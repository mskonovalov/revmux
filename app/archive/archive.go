// Package archive writes the artifacts of one review run into the round directory the caller prepared.
//
// A round holds the caller's own input/ beside them, which is what makes the review auditable from the
// round alone: the composed prompt each process actually received, the findings after each stage,
// revmux's own decisions, and every agent's verbatim stream. Nothing above the round is written, and
// nothing anywhere is deleted.
package archive

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/umputun/revmux/app/task"
)

// Archive is one round's artifact directory.
//
// The round is held as an open handle rather than re-opened by name on every write, and the chain down
// to it starts at the tasks root. A path validated once and then reopened is a path another process can
// swap for a symlink in between, and every artifact of this round would follow it. A handle keeps
// pointing at the directory it was opened on, and refuses any name that leaves it.
//
// The marker is held open and locked for the same duration, which is what says this round is one run's.
type Archive struct {
	dir    string   // this round's directory, for error messages
	root   *os.Root // handle on dir, every artifact is written through it
	marker *os.File // manifest.json, held locked so no second run claims this round
}

// New opens the round <TasksDir>/<Task>/<Run> and claims it by creating manifest.json exclusively, so a
// round that has already run is refused rather than overwritten: a round that went badly is exactly the
// one a later reflection agent wants to read.
//
// It walks down from the tasks root as nested os.Roots rather than opening the joined path, so every hop
// is contained by the one above it however a symlink got there. Nothing on the way is created — the
// tasks root, the task directory and the round with the input/ the caller filled are all his, and revmux
// authors no part of the context it reviews.
func New(opts task.Round) (*Archive, error) {
	if opts.TasksDir == "" {
		return nil, errors.New("tasks directory is empty")
	}
	if err := task.CheckName("--task", opts.Task); err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	if err := task.CheckRoundName("--run", opts.Run); err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}

	tasks, err := filepath.Abs(opts.TasksDir)
	if err != nil {
		return nil, fmt.Errorf("resolve tasks directory %q: %w", opts.TasksDir, err)
	}
	tasksRoot, err := os.OpenRoot(tasks)
	if err != nil {
		return nil, fmt.Errorf("open tasks directory %s: %w", tasks, err)
	}
	defer tasksRoot.Close() // nothing was written through it, only the round's own handle outlives New

	// opened, never created: the caller writes the task directory, so a missing one is his error to fix and
	// not a directory for revmux to author. Opening also resolves a task legitimately reached through a
	// relative symlink inside the tasks root, which a MkdirAll on the same name would refuse outright.
	taskPath := filepath.Join(tasks, opts.Task)
	taskRoot, err := tasksRoot.OpenRoot(opts.Task)
	if err != nil {
		return nil, fmt.Errorf("open task directory %s inside the tasks root %s: %w", taskPath, tasks, err)
	}
	defer taskRoot.Close() // same, it only carries the walk down to the round

	dir := filepath.Join(taskPath, opts.Run)
	if entryErr := checkRoundEntry(taskRoot, opts.Run, dir); entryErr != nil {
		return nil, entryErr
	}
	root, err := taskRoot.OpenRoot(opts.Run)
	if err != nil {
		return nil, fmt.Errorf("open round directory %s: %w", dir, err)
	}
	if err = checkHandle(taskRoot, root, opts.Run, dir); err != nil {
		root.Close() //nolint:gosec // the handle is being refused, and nothing was ever written through it
		return nil, err
	}
	if err = requireInput(root, dir); err != nil {
		root.Close() //nolint:gosec // same, the round is unusable and the error names why
		return nil, err
	}
	marker, err := claimRound(root, dir)
	if err != nil {
		root.Close() //nolint:gosec // same, and the round this refused keeps every artifact it had
		return nil, err
	}
	return &Archive{dir: dir, root: root, marker: marker}, nil
}

// Close releases the round's directory handle and the claim on its marker. The artifacts are already on
// disk by then; this only drops the descriptors that kept the round pinned for the duration of the run,
// and dropping the marker is what lets a later run re-claim a round this one never finished.
func (a *Archive) Close() error {
	if a.marker != nil {
		if err := a.marker.Close(); err != nil {
			return fmt.Errorf("close %s: %w", filepath.Join(a.dir, task.ManifestFile), err)
		}
		a.marker = nil // run defers Close, so a caller closing early must not fail the run
	}
	if err := a.root.Close(); err != nil {
		return fmt.Errorf("close archive %s: %w", a.dir, err)
	}
	return nil
}

// Writer opens one artifact for writing, creating parent directories as needed. name is a path
// relative to the round directory, so one method serves the per-agent tees, the composed prompts, the
// per-stage findings, the manifest and the rendered report without the archive knowing what any of
// them mean.
//
// A separator is legal — prompts/agents/, stages/ and agents/ all need one — and only a path leaving
// the round directory is rejected. The handle settles that: a symlink inside the round pointing anywhere
// else is refused when it is traversed, not when it was last looked at.
func (a *Archive) Writer(name string) (io.WriteCloser, error) {
	clean, err := a.resolve(name)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(clean); dir != "." {
		if mkErr := a.root.MkdirAll(dir, 0o750); mkErr != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Join(a.dir, dir), mkErr)
		}
	}
	f, err := a.root.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Join(a.dir, clean), err)
	}
	return f, nil
}

// resolve reduces a round-relative artifact path to the clean name the round handle takes. The handle
// rejects an escape through a symlink on its own; what it cannot judge is a name that was never an
// artifact path to begin with — absolute, empty, or climbing out lexically.
func (a *Archive) resolve(name string) (string, error) {
	if name == "" {
		return "", errors.New("artifact name is empty")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("artifact %q is absolute", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact %q escapes the round directory", name)
	}
	return clean, nil
}

// checkRoundEntry reads the round entry under the task directory and refuses a symlink: every artifact
// of this round would be written wherever it points. Containment alone does not settle that — os.Root
// follows a link landing back inside the root, so a round linked to a sibling round passes every
// containment check and still truncates that round's artifacts.
//
// A missing entry is the caller's to fix rather than revmux's to create, since the round carries the
// input/ this review reads.
func checkRoundEntry(taskRoot *os.Root, name, path string) error {
	fi, err := taskRoot.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("round directory %s is not there: it holds this round's %s/, so the caller creates it",
			path, task.InputDir)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, and revmux writes only a real directory it was handed", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// checkHandle proves an open handle is the entry it was opened by name from, rather than whatever a
// symlink resolved to. Looking at an entry and opening it are two operations, so it can be swapped in
// between and the handle would pin the link's target for the rest of the run — the one thing
// checkRoundEntry exists to refuse, reached by racing it. Reading the entry again and matching it against
// the directory actually opened closes that window: a swap after this point leaves the handle on what
// this check accepted.
//
// A round swapped for a link to an earlier one pins that round, and every artifact this run writes
// truncates one of its own — destroying exactly the bad round a reflection agent wants to read.
func checkHandle(parent, opened *os.Root, name, path string) error {
	entry, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, and revmux writes only a real directory it was handed", path)
	}
	got, err := opened.Stat(".")
	if err != nil {
		return fmt.Errorf("stat open %s: %w", path, err)
	}
	if !os.SameFile(entry, got) {
		return fmt.Errorf("%s was replaced while it was being opened", path)
	}
	return nil
}

// requireInput refuses a round the caller has not filled. input/ is the only channel review context
// travels through, so a round without one carries no scope and there is nothing to review.
//
// The entry is read with Lstat, and a symlink is refused rather than followed: os.Root resolves a link
// landing back inside the round, so an input/ aliased onto another directory passes containment and makes
// this round's archived context a pointer at somebody else's. `revmux new` refuses the same shape, and the
// two have to agree about which rounds are usable.
func requireInput(round *os.Root, dir string) error {
	path := filepath.Join(dir, task.InputDir)
	fi, err := round.Lstat(task.InputDir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is not there, and it is what carries this round's review context", path)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, and revmux reads only a real directory it was handed", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// claimRound takes the round for this run and returns the marker it holds it by, open and locked until
// Close. The exclusive create is what detects a round that has already run — atomic where a look followed
// by a write is not, and it leaves every artifact of the earlier round exactly as it was.
//
// The lock is taken on the marker this run just created as well, because creating it and locking it are
// two operations: a racer reading the fresh marker in between finds it empty and would otherwise reclaim
// it. Both callers lock, so whichever gets there first owns the round and the other is refused.
//
// A marker already there is reclaim's question.
func claimRound(round *os.Root, dir string) (*os.File, error) {
	path := filepath.Join(dir, task.ManifestFile)
	f, err := round.OpenFile(task.ManifestFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return reclaim(round, dir, path)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if err := hold(f, dir, path); err != nil {
		f.Close() //nolint:gosec // the round is refused, and the marker this created carries nothing
		return nil, err
	}
	return f, nil
}

// hold takes this run's claim on an open marker and keeps it for as long as the descriptor lives. A lock
// already held means another run is writing this round right now, and sharing it would have both runs
// truncate the same artifacts until the last manifest won.
func hold(f *os.File, dir, path string) error {
	ok, err := tryLock(f)
	if err != nil {
		return fmt.Errorf("claim %s: %w", path, err)
	}
	if !ok {
		return fmt.Errorf("round %s is being written by a run holding it: two runs sharing a round truncate "+
			"each other's artifacts, so open a new round instead", dir)
	}
	return nil
}

// reclaim decides whether a round already carrying a marker may be taken over by this run.
//
// The marker is classified by task.CheckMarker, the one definition `revmux new` gates on too. A filled one
// is a round that ran and is refused. An empty one means the round was claimed and says nothing about
// whether that run is still going, so the lock settles it: taken means a live run owns the round, free
// means the run that claimed it never came back and the round is re-claimed rather than burnt — the
// caller's own input/ lives in it. Only while that run left nothing else behind, which task.CheckReclaim
// decides, and it is asked under the lock so two racers cannot both pass it.
//
// The entry is read with Lstat: os.Root.Stat follows a link that lands back inside the round, so a
// manifest.json pointing at the caller's own goal.md would read as its size here and be truncated by the
// write that fills the marker in. The same read is repeated once the lock is held, against the descriptor
// as well as the entry, since everything looked at before it could be swapped in between.
func reclaim(round *os.Root, dir, path string) (*os.File, error) {
	ran, err := readMarker(round, path)
	if err != nil {
		return nil, err
	}
	if ran {
		return nil, alreadyRan(dir)
	}

	f, err := round.OpenFile(task.ManifestFile, os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err = takeOver(round, f, dir, path); err != nil {
		f.Close()       //nolint:gosec // the round is refused, nothing was written through this, and every
		return nil, err // artifact it already had is left exactly where it was
	}
	return f, nil
}

// takeOver holds the claim on an open marker and re-establishes under the lock everything the decision to
// reclaim rested on. Each of those was read before the lock was held, so each could have changed while it
// was being waited for: the run that claimed the round may have finished, the marker may have been
// swapped, and another reclaimer may have started writing into the round.
func takeOver(round *os.Root, f *os.File, dir, path string) error {
	if err := hold(f, dir, path); err != nil {
		return err
	}
	if err := checkMarkerHandle(round, f, path, dir); err != nil {
		return err
	}
	entries, err := fs.ReadDir(round.FS(), ".")
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if err = task.CheckReclaim(dir, entries); err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	return nil
}

// readMarker reads the round's own manifest.json entry and reports whether it is a round that ran.
func readMarker(round *os.Root, path string) (ran bool, err error) {
	fi, err := round.Lstat(task.ManifestFile)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	ran, err = task.CheckMarker(path, fi)
	if err != nil {
		return false, fmt.Errorf("open archive: %w", err)
	}
	return ran, nil
}

// checkMarkerHandle re-reads the marker once the lock is held and proves the descriptor is still the entry
// it was opened by name from. The run that claimed the round may have finished between the read above and
// the lock, which makes this a round that ran; and the entry may have been swapped for a link, which the
// write filling the marker in would follow.
func checkMarkerHandle(round *os.Root, f *os.File, path, dir string) error {
	ran, err := readMarker(round, path)
	if err != nil {
		return err
	}
	if ran {
		return alreadyRan(dir)
	}
	entry, err := round.Lstat(task.ManifestFile)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	got, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat open %s: %w", path, err)
	}
	if !os.SameFile(entry, got) {
		return fmt.Errorf("%s was replaced while the round was being claimed", path)
	}
	return nil
}

func alreadyRan(dir string) error {
	return fmt.Errorf("round %s has already run, %s is in place: a round that went badly is exactly the one "+
		"a later reflection agent reads", dir, task.ManifestFile)
}
