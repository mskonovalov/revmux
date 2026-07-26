// Package archive writes the artifacts of one review run under the task directory's runs/<run>/.
//
// Everything above runs/ was written by the caller and is never modified or pruned. The artifacts
// exist so a review can be audited without re-running it: the composed prompt each process actually
// received, the findings after each stage, revmux's own decisions, and every agent's verbatim stream.
package archive

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// runsDir is the one directory revmux owns inside a task directory.
const runsDir = "runs"

// Opts is what an archive needs. The names travel as fields rather than positionally: New(tasksDir,
// task, run) would put three same-typed strings side by side, and swapping any two compiles clean while
// writing the run somewhere else entirely.
//
// The task is named by its root and its own name rather than as one joined path, because that is what
// lets New anchor the whole chain at the tasks root instead of trusting a path string it would have to
// reopen by name.
type Opts struct {
	TasksDir string
	Task     string
	Run      string
	Keep     int
}

// Archive is one run's artifact directory.
//
// Every directory on the way down is held as an open handle rather than re-opened by name on every
// write, and the chain starts at the tasks root. A path validated once and then reopened is a path
// another process can swap for a symlink in between, and what would follow that symlink is Prune, the
// one destructive primitive in the tool. A handle keeps pointing at the directory it was opened on, and
// refuses any name that leaves it.
type Archive struct {
	dir  string   // this run's directory, for error messages
	root *os.Root // handle on dir, every artifact is written through it, and what Prune excludes by identity
	runs *os.Root // handle on runs/, what Prune enumerates and deletes from
	keep int
}

// runEntry is one prior run directory as Prune sees it, named relative to runs/ because that is what
// the handle deleting it takes. The identity travels with the name: RemoveAll takes a name, so the name
// alone would let the pass delete whatever carries it by the time the deletion runs.
type runEntry struct {
	name string
	info fs.FileInfo
}

// New creates <TasksDir>/<Task>/runs/<Run> and fails when it already exists rather than overwriting it: a
// round that went badly is exactly the one a later reflection agent wants to read.
//
// It walks down from the tasks root as nested os.Roots rather than opening the joined path, so every hop
// is contained by the one above it however a symlink got there. Only runs/ and the run directory are
// created; the tasks root and the task directory must already be there, since everything above runs/ is
// the caller's and revmux never authors a task it did not receive.
func New(opts Opts) (*Archive, error) {
	if opts.TasksDir == "" {
		return nil, errors.New("tasks directory is empty")
	}
	if err := checkComponent("task name", opts.Task); err != nil {
		return nil, err
	}
	if err := checkComponent("run name", opts.Run); err != nil {
		return nil, err
	}

	tasks, err := filepath.Abs(opts.TasksDir)
	if err != nil {
		return nil, fmt.Errorf("resolve tasks directory %q: %w", opts.TasksDir, err)
	}
	tasksRoot, err := os.OpenRoot(tasks)
	if err != nil {
		return nil, fmt.Errorf("open tasks directory %s: %w", tasks, err)
	}
	defer tasksRoot.Close() // nothing was written through it, only the run's own handles outlive New

	// opened, never created: the caller writes the task directory, so a missing one is his error to fix and
	// not a directory for revmux to author. Opening also resolves a task legitimately reached through a
	// relative symlink inside the tasks root, which a MkdirAll on the same name would refuse outright.
	task := filepath.Join(tasks, opts.Task)
	taskRoot, err := tasksRoot.OpenRoot(opts.Task)
	if err != nil {
		return nil, fmt.Errorf("open task directory %s inside the tasks root %s: %w", task, tasks, err)
	}
	defer taskRoot.Close() // same, it only carries the walk down to runs/

	runsPath := filepath.Join(task, runsDir)
	if runsErr := checkRunsEntry(taskRoot, runsPath); runsErr != nil {
		return nil, runsErr
	}
	if mkErr := taskRoot.MkdirAll(runsDir, 0o750); mkErr != nil {
		return nil, fmt.Errorf("create %s: %w", runsPath, mkErr)
	}
	runs, err := taskRoot.OpenRoot(runsDir)
	if err != nil {
		return nil, fmt.Errorf("runs directory escapes the task directory %s: %w", task, err)
	}
	if err = checkHandle(taskRoot, runs, runsPath); err != nil {
		runs.Close() //nolint:gosec // the handle is being refused, and nothing was ever written through it
		return nil, err
	}

	dir := filepath.Join(runsPath, opts.Run)
	if err = runs.Mkdir(opts.Run, 0o750); err != nil {
		runs.Close() //nolint:gosec // the run was never created, so there is nothing to report about the handle
		return nil, fmt.Errorf("create run directory %s: %w", dir, err)
	}
	root, err := runs.OpenRoot(opts.Run)
	if err != nil {
		runs.Close() //nolint:gosec // same, the run directory is unusable and the error names why
		return nil, fmt.Errorf("open run directory %s: %w", dir, err)
	}
	if err = checkHandle(runs, root, dir); err != nil {
		root.Close() //nolint:gosec // both handles are refused, and nothing was ever written through either
		runs.Close() //nolint:gosec // same
		return nil, err
	}
	return &Archive{dir: dir, root: root, runs: runs, keep: opts.Keep}, nil
}

// Close releases the two directory handles. The artifacts are already on disk by then; this only drops
// the descriptors that kept the run directory and runs/ pinned for the duration of the run.
func (a *Archive) Close() error {
	if err := errors.Join(a.root.Close(), a.runs.Close()); err != nil {
		return fmt.Errorf("close archive %s: %w", a.dir, err)
	}
	return nil
}

// Writer opens one artifact for writing, creating parent directories as needed. name is a path
// relative to the run directory, so one method serves the per-agent tees, the composed prompts, the
// per-stage findings, the manifest and the rendered report without the archive knowing what any of
// them mean.
//
// A separator is legal — prompts/agents/, stages/ and agents/ all need one — and only a path leaving
// the run directory is rejected. The handle settles that: a symlink inside the run pointing anywhere
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

// Prune drops the oldest runs beyond Keep. It reads only runs/, so scope.md, goal.md, profile.md and
// context/ are never candidates however aggressive Keep is, and the run being written is never one.
//
// That last exclusion goes by the directory the run handle is on, not by the name the run was created
// under. The two differ the moment something renames the run directory mid-run, and then a name match
// excludes nothing: this run's own archive becomes an ordinary candidate, and at Keep 0 or 1 Prune
// deletes the artifacts it was called to make room for. The handle is the same authority every other
// check in this package leans on.
//
// Enumerating by identity settles only half of it, because what deletes a candidate is its name. Each
// one therefore carries the directory it was enumerated as, and remove empties it through a handle
// before unlinking the name — see there for what a rename in between would otherwise cost.
func (a *Archive) Prune() error {
	self, err := a.root.Stat(".")
	if err != nil {
		return fmt.Errorf("stat %s: %w", a.dir, err)
	}

	d, err := a.runs.Open(".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Dir(a.dir), err)
	}
	entries, err := d.ReadDir(-1)
	d.Close() //nolint:gosec // read-only, and the entries are already in hand
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Dir(a.dir), err)
	}

	old := make([]runEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %s: %w", a.runPath(e.Name()), infoErr)
		}
		if os.SameFile(info, self) {
			continue // the run being written, under whatever name it now carries
		}
		old = append(old, runEntry{name: e.Name(), info: info})
	}

	// the run being written counts toward keep, so one slot is already taken
	allowed := max(a.keep-1, 0)
	if len(old) <= allowed {
		return nil
	}

	slices.SortFunc(old, func(x, y runEntry) int { return x.info.ModTime().Compare(y.info.ModTime()) })
	for _, r := range old[:len(old)-allowed] {
		if err := a.remove(r); err != nil {
			return err
		}
	}
	return nil
}

// remove drops one prior run. The exclusion in Prune is by identity, but a deletion takes a name and there
// is no unlink-by-handle to take instead, so a name that changed hands between the enumeration and the
// deletion carries the deletion to whatever now answers to it — including the run being written, the one
// directory the identity check exists to spare.
//
// RemoveAll on that name would take the whole tree under it. So the recursive part happens first and
// through a handle opened on the candidate itself, and what the name is left to do is a single
// non-recursive removal: an entry that changed hands in that last window is at worst an empty directory,
// never a populated one, and a live run directory refuses the unlink outright because it is not empty.
func (a *Archive) remove(r runEntry) error {
	if err := a.clear(r); err != nil {
		return err
	}
	ok, err := a.enumerated(r)
	if err != nil {
		return err
	}
	if !ok {
		return nil // gone already, or some other directory answers to the name now
	}
	if err := a.runs.Remove(r.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", a.runPath(r.name), err)
	}
	return nil
}

// clear empties one candidate through a handle opened on it, and only when that handle landed on the
// directory the pass enumerated. A handle keeps referring to the directory it was opened on however the
// name moves afterwards, so every recursive step below is anchored on the candidate rather than on a name
// another process can redirect. Entries are read as they are on disk: a symlink is unlinked, never
// followed, so nothing outside the candidate is reachable from here.
func (a *Archive) clear(r runEntry) error {
	ok, err := a.enumerated(r)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	path := a.runPath(r.name)
	dir, err := a.runs.OpenRoot(r.name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer dir.Close()

	got, err := dir.Stat(".")
	if err != nil {
		return fmt.Errorf("stat open %s: %w", path, err)
	}
	if !os.SameFile(got, r.info) {
		return nil // the name was redirected between the look and the open
	}
	return a.clearDir(dir, path)
}

// clearDir deletes everything under one open directory, depth first, naming entries only relative to the
// handle they were read from. path is for error messages.
func (a *Archive) clearDir(dir *os.Root, path string) error {
	d, err := dir.Open(".")
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	entries, err := d.ReadDir(-1)
	d.Close() //nolint:gosec // read-only, and the entries are already in hand
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	for _, e := range entries {
		child := filepath.Join(path, e.Name())
		if e.IsDir() { // a symlink is never one here, ReadDir reports the entry rather than its target
			if err := a.clearSub(dir, e.Name(), child); err != nil {
				return err
			}
		}
		if err := dir.Remove(e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", child, err)
		}
	}
	return nil
}

// clearSub descends into one subdirectory, opening it before deleting anything inside so the recursion
// stays anchored the same way clear is. Contained by the handle above it, so the worst a swap underneath
// can reach is another entry of the candidate being deleted anyway.
func (a *Archive) clearSub(dir *os.Root, name, path string) error {
	sub, err := dir.OpenRoot(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer sub.Close()

	return a.clearDir(sub, path)
}

// enumerated reports whether the candidate's name still leads to the directory this pass enumerated under
// it. Lstat rather than Stat: a symlink swapped in carries its own identity and must not answer for what
// it points at, since unlinking the name would then leave the directory behind.
func (a *Archive) enumerated(r runEntry) (bool, error) {
	fi, err := a.runs.Lstat(r.name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // already gone, which is what this pass wanted of it
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", a.runPath(r.name), err)
	}
	return os.SameFile(fi, r.info), nil
}

// resolve reduces a run-relative artifact path to the clean name the run handle takes. The handle
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
		return "", fmt.Errorf("artifact %q escapes the run directory", name)
	}
	return clean, nil
}

// runPath names a sibling run for an error message: the handles work on names, the messages on paths.
func (a *Archive) runPath(name string) string {
	return filepath.Join(filepath.Dir(a.dir), name)
}

// checkRunsEntry reads the runs entry under the task directory and refuses a symlink: this run would be
// written wherever it points and Prune would delete from there. Containment alone does not settle that —
// os.Root follows a link landing back inside the root, so runs -> context passes every containment check
// and still hands Prune the caller's own directories. A missing entry is fine, MkdirAll creates it.
func checkRunsEntry(taskRoot *os.Root, path string) error {
	fi, err := taskRoot.Lstat(runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // absent is not an entry and not a fault, the caller creates it next
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, and revmux writes and prunes only a real directory it owns", path)
	}
	return nil
}

// checkHandle proves an open handle is the entry it was opened by name from, rather than whatever a
// symlink resolved to. Creating or looking at an entry and opening it are two operations, so it can be
// swapped in between and the handle would pin the link's target for the rest of the run — the one thing
// checkRunsEntry exists to refuse, reached by racing it. Reading the entry again and matching it against
// the directory actually opened closes that window: a swap after this point leaves the handle on what
// this check accepted.
//
// Both hops below runs/ need it. A swapped runs/ hands Prune the caller's own directories; a swapped run
// directory pins an earlier round, and every artifact this run writes truncates one of its own.
//
// path is the joined path the handle was opened at, and the entry name is its last component rather than a
// fourth parameter: the two are the same string at both call sites, and passing them separately puts a
// second same-typed pair next to the two roots for nothing.
func checkHandle(parent, opened *os.Root, path string) error {
	name := filepath.Base(path)
	entry, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, and revmux writes and prunes only a real directory it owns", path)
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

// checkComponent rejects a caller-supplied name before it becomes one path component. It is the same
// rule package main applies to --task and --run, repeated here because Archive is reachable on its
// own and a run named `..` would write over the caller's context.
func checkComponent(what, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%s is empty", what)
	case filepath.IsAbs(name):
		return fmt.Errorf("%s %q is absolute", what, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%s %q contains a path separator", what, name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%s %q references a parent directory", what, name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%s %q starts with a dot", what, name)
	}
	return nil
}
