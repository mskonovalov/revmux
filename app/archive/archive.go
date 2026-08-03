// Package archive writes the artifacts of one review run into the round directory the caller prepared:
// the composed prompts, the per-stage findings, revmux's own decisions and every agent's verbatim
// stream, beside the caller's own input/. Nothing above the round is written, and Cleanup is the one
// thing here that removes anything.
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

// Archive is one round's artifact directory. The round is held as an open handle rather than reopened by
// name on every write, since a path validated once and then reopened can be swapped for a symlink in
// between. The marker is held open and locked for the same duration, which says the round is this run's.
type Archive struct {
	dir    string   // this round's directory, for error messages
	root   *os.Root // handle on dir, every artifact is written through it
	marker *os.File // manifest.json, held locked so no second run claims this round
}

// New opens the round <TasksDir>/<Task>/<Run> and claims it by creating manifest.json exclusively, so a
// round that has already run is refused rather than overwritten. It walks down from the tasks root as
// nested os.Roots, so every hop is contained by the one above it. Nothing on the way is created.
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
	defer tasksRoot.Close() // only the round's own handle outlives New

	// opened, never created. Opening also resolves a task legitimately reached through a relative symlink
	// inside the tasks root, which a MkdirAll on the same name would refuse outright.
	taskPath := filepath.Join(tasks, opts.Task)
	taskRoot, err := tasksRoot.OpenRoot(opts.Task)
	if err != nil {
		return nil, fmt.Errorf("open task directory %s inside the tasks root %s: %w", taskPath, tasks, err)
	}
	defer taskRoot.Close() // same, it only carries the walk down

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

// Close releases the round's directory handle and the claim on its marker. Dropping the marker is what
// lets a later run re-claim a round this one never finished.
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

// Writer opens one artifact for writing, creating parent directories as needed. name is relative to the
// round directory. A separator is legal — prompts/agents/, stages/ and agents/ all need one — and only a
// path leaving the round is rejected; the handle refuses a symlink when it is traversed.
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

// checkRoundEntry reads the round entry under the task directory and refuses a symlink. Containment
// alone does not settle it: os.Root follows a link landing back inside the root, so a round linked to a
// sibling passes every containment check and still truncates that round's artifacts.
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
// between; a swap after this check leaves the handle on what the check accepted.
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

// requireInput refuses a round the caller has not filled. The entry is read with Lstat and a symlink is
// refused rather than followed: os.Root resolves a link landing back inside the round, so an aliased
// input/ passes containment and makes this round's archived context a pointer at somebody else's.
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
// Close. The exclusive create is what detects a round that has already run. The lock is taken on a marker
// this run just created too: creating and locking are two operations, and a racer in between finds it
// empty. A marker already there is reclaim's question.
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
// already held means another run is writing this round right now.
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

// reclaim decides whether a round already carrying a marker may be taken over by this run. A filled
// marker is a round that ran and is refused; an empty one says nothing about whether that run is still
// going, so the lock settles it. The entry is read with Lstat: os.Root.Stat follows a link landing back
// inside the round, so a manifest.json aliasing the caller's goal.md would read as its size.
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
// reclaim rested on, each of which was read before the lock was held and could have changed since.
func takeOver(round *os.Root, f *os.File, dir, path string) error {
	if err := hold(f, dir, path); err != nil {
		return err
	}
	if err := checkMarkerHandle(round, f, dir, path); err != nil {
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

// checkMarkerHandle re-reads the marker once the lock is held and proves the descriptor is still the
// entry it was opened by name from. The claiming run may have finished between the read above and the
// lock, and the entry may have been swapped for a link the marker write would follow.
func checkMarkerHandle(round *os.Root, f *os.File, dir, path string) error {
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
