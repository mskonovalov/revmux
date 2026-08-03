package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/umputun/revmux/app/task"
)

// CleanupQuery names the one task to remove. There is no all-tasks form and no age or size threshold:
// what to reclaim is the user's decision, from numbers `revmux stats` already reports.
type CleanupQuery struct {
	TasksDir string
	Task     string
}

// CleanupResult is what Cleanup removed and what the tasks root costs afterwards. Removed is an array
// holding the one task this call took, so a caller reading it need not special-case the single-task shape
// against the numbers `revmux stats` reports as arrays.
type CleanupResult struct {
	TasksDir string    `json:"tasks_dir"`
	Removed  []Removal `json:"removed"`

	// TotalMB is absent when the root could not be measured after the removal. That measurement happens
	// after the tree is gone, so its failure may not fail the call — the removal succeeded, and a caller
	// told otherwise reports a task that is in fact removed.
	TotalMB *float64 `json:"total_mb_after,omitempty"`
}

// Removal is one task that is gone, measured before it went: after the tree is removed there is nothing
// left to read these back from, and they are the caller's only record of what he gave up.
type Removal struct {
	ID     string  `json:"id"`
	Rounds int     `json:"rounds"`
	SizeMB float64 `json:"size_mb"`
}

// Cleanup is the sole destructive operation in revmux, and it takes a whole task rather than a round: a
// task's rounds are one review's history and a reflection agent reads them together.
//
// It refuses a task any round of which a live run holds, which is the marker lock claimRound takes rather
// than a pid or a timestamp. That is a check and nothing more — the locks are released as it goes, so a
// review claiming a round between the check and the removal loses it. See CLAUDE.md for why that ceiling
// is deliberate.
func Cleanup(q CleanupQuery) (CleanupResult, error) {
	if q.Task == "" {
		return CleanupResult{}, errors.New("no task named: pass --task with the id to remove")
	}
	if err := task.CheckName("--task", q.Task); err != nil {
		return CleanupResult{}, fmt.Errorf("check task name: %w", err)
	}

	// task.List is the only enumeration of what a task is, so this removes exactly what `revmux stats` and
	// `revmux config` report. A name not in it is an error: an empty removal reads as a task already gone.
	ids, err := task.List(q.TasksDir)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("list tasks: %w", err)
	}
	if !slices.Contains(ids, q.Task) {
		return CleanupResult{}, fmt.Errorf("task %s not found under %s", q.Task, q.TasksDir)
	}

	// the removal is contained by a handle on the tasks root rather than by the name having been checked:
	// a joined path is only as contained as the instant it was measured, and this walks a whole tree
	root, err := os.OpenRoot(q.TasksDir)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("open tasks root %s: %w", q.TasksDir, err)
	}
	defer root.Close()

	if aliasErr := q.checkAlias(root); aliasErr != nil {
		return CleanupResult{}, aliasErr
	}

	taskRoot, err := root.OpenRoot(q.Task)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("open task %s: %w", q.Task, err)
	}
	err = q.checkIdle(taskRoot)
	_ = taskRoot.Close()
	if err != nil {
		return CleanupResult{}, err
	}

	entry, err := q.describe()
	if err != nil {
		return CleanupResult{}, err
	}

	// RemoveAll is not atomic, so a failure may leave some of the task behind. The error says it may,
	// never that it did: from here the two are indistinguishable.
	if err = root.RemoveAll(q.Task); err != nil {
		return CleanupResult{}, fmt.Errorf("remove task %s: %w (some of it may remain at %s)",
			q.Task, err, filepath.Join(q.TasksDir, q.Task))
	}

	// past this line the task is gone and the call succeeded, so a failure measuring what is left omits
	// the number rather than discarding the record of what went
	out := CleanupResult{TasksDir: q.TasksDir, Removed: []Removal{entry}}
	if after, _, sizeErr := dirSize(q.TasksDir); sizeErr == nil {
		total := mb(after)
		out.TotalMB = &total
	}
	return out, nil
}

// checkAlias refuses a task directory that is a symlink, naming what it points at. task.List accepts one
// deliberately, but the three things a removal does disagree about what such an id names: task.Rounds
// follows the link, dirSize lstats it and reports zero, and RemoveAll unlinks it rather than descending.
func (q CleanupQuery) checkAlias(root *os.Root) error {
	fi, err := root.Lstat(q.Task)
	if err != nil {
		return fmt.Errorf("read task %s: %w", q.Task, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := root.Readlink(q.Task)
	if err != nil {
		return fmt.Errorf("task %s is a symlink to another task and will not be removed under this name", q.Task)
	}
	return fmt.Errorf("task %s is a symlink to %s and will not be removed under this name: "+
		"removing it would unlink the alias while every round stays on disk, so pass the id it points at",
		q.Task, target)
}

// describe measures what is about to go, before it goes: after RemoveAll there is nothing left to read
// the numbers from. Rounds is task.Rounds, so a round prepared but never reviewed is removed with the
// task and is not in this number — the same enumeration `revmux stats` and `revmux config` report.
func (q CleanupQuery) describe() (Removal, error) {
	dir := filepath.Join(q.TasksDir, q.Task)
	// unread entries leave the size a floor rather than blocking the removal: a task holding one
	// unreadable directory must still be removable
	size, _, err := dirSize(dir)
	if err != nil {
		return Removal{}, err
	}
	rounds, err := task.Rounds(dir)
	if err != nil {
		return Removal{}, fmt.Errorf("list rounds of task %s: %w", q.Task, err)
	}
	return Removal{ID: q.Task, Rounds: len(rounds), SizeMB: mb(size)}, nil
}

// checkIdle refuses the task while any of its rounds is claimed by a run that is still going. It tries
// the lock claimRound holds, on every round carrying a marker rather than only the ones HasRun accepts:
// a live run's marker is still empty. It catches a review running now, not one starting a moment later.
func (q CleanupQuery) checkIdle(taskRoot *os.Root) error {
	dir := filepath.Join(q.TasksDir, q.Task)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read task %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		roundRoot, rootErr := taskRoot.OpenRoot(e.Name())
		if rootErr != nil {
			continue // not a directory this call can descend, so no marker of its own to hold
		}
		fi, statErr := roundRoot.Lstat(task.ManifestFile)
		if statErr != nil || !fi.Mode().IsRegular() {
			_ = roundRoot.Close()
			continue
		}
		f, openErr := roundRoot.OpenFile(task.ManifestFile, os.O_WRONLY, 0)
		_ = roundRoot.Close()
		if openErr != nil {
			continue // unreadable is an IO or permission fault, not the signature of a live run
		}
		ok, lockErr := tryLock(f)
		_ = f.Close() // the close drops the lock this check just took
		if lockErr != nil {
			return fmt.Errorf("check round %s of task %s: %w", e.Name(), dir, lockErr)
		}
		if !ok {
			return fmt.Errorf("round %s of task %s is held by a running review: nothing was removed", e.Name(), dir)
		}
	}
	return nil
}
