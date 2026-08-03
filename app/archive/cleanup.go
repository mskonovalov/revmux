package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/umputun/revmux/app/task"
)

// CleanupQuery names the one task to remove. There is no all-tasks form and no age or size threshold: what
// to reclaim is the user's decision, and a command that could pick for him would be picking from numbers
// `revmux stats` already reports to whoever is asking.
type CleanupQuery struct {
	TasksDir string
	Task     string
}

// Cleanup is the sole destructive operation in revmux, and it is dedicated to being one: nothing else —
// not a review, not `new`, not `init`, not `stats` — removes anything, so nothing removes anything as a
// side effect of doing something else.
//
// It removes the whole task rather than a round inside it. A task's rounds are one review's history and a
// reflection agent reads them together, so a task that silently lost its early rounds is worse than one
// that is gone: the numbers `revmux stats` derives from what is left read as the whole record.
//
// It refuses a task any round of which a live run holds, which is the marker lock claimRound takes rather
// than a pid or a timestamp — the kernel drops that lock when the process dies, so an abandoned round is
// distinguishable from one being written right now.
//
// The check is a check and nothing more: the locks are released as it goes, so a review that claims a round
// between the check and the removal loses that round. Holding them across the removal would close that
// window and cost the plumbing to carry the handles — not a trade worth making for a command that removes
// old review logs, where the loss is one interrupted run of a review the user can start again.
func Cleanup(q CleanupQuery) (CleanupResult, error) {
	if q.Task == "" {
		return CleanupResult{}, errors.New("no task named: pass --task with the id to remove")
	}
	if err := task.CheckName("--task", q.Task); err != nil {
		return CleanupResult{}, fmt.Errorf("check task name: %w", err)
	}

	// task.List is the only enumeration of what a task is, so this removes exactly what `revmux stats` and
	// `revmux config` report. A name that is not in it — a typo, an entry the archive cannot walk — is an
	// error rather than a no-op: answered with an empty removal it reads as a task already gone.
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

	// RemoveAll is not atomic — it records the first error and keeps unlinking — so a failure may leave
	// some of the task behind. The error says that much and no more: whether anything was actually removed
	// is not knowable from here, and a message that guessed would be wrong half the time. What is left is
	// the user's to look at and remove, which is what it was before this command existed.
	if err = root.RemoveAll(q.Task); err != nil {
		return CleanupResult{}, fmt.Errorf("remove task %s: %w (some of it may remain at %s)",
			q.Task, err, filepath.Join(q.TasksDir, q.Task))
	}

	// past this line the task is gone and the call has succeeded, so a failure measuring what is left omits
	// the number rather than discarding the record of what went
	out := CleanupResult{TasksDir: q.TasksDir, Removed: []Removal{entry}}
	if after, _, sizeErr := dirSize(q.TasksDir); sizeErr == nil {
		total := mb(after)
		out.TotalMB = &total
	}
	return out, nil
}

// checkAlias refuses a task directory that is a symlink, naming what it points at.
//
// task.List accepts one deliberately — a relative link to a sibling task is an alias the review path,
// `revmux new` and `revmux config` all follow — but the three things a removal does disagree about what
// such an id names: task.Rounds follows the link and counts the target's rounds, dirSize lstats its root
// and reports zero, and RemoveAll unlinks the link rather than descending it. The payload then states that
// five rounds went and that it freed nothing, of a task still entirely on disk.
//
// Refusing is the honest resolution rather than resolving and removing the target: the id the user typed
// is not the task, and a command that removed something under a different name than the one it was given
// is the failure this whole file is written against.
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

// describe measures what is about to go, before it goes: the numbers are the caller's record of what he
// gave up, and after RemoveAll there is nothing left to read them from.
//
// Rounds is task.Rounds, so it counts the rounds that ran — a round prepared but never reviewed, or one an
// interrupted run left claimed, is removed with the task and is not in this number. That is the same
// enumeration `revmux stats` and `revmux config` report, and the payload is read beside theirs.
func (q CleanupQuery) describe() (Removal, error) {
	dir := filepath.Join(q.TasksDir, q.Task)
	// unread entries leave the size a floor rather than blocking the removal: a task holding one
	// unreadable directory must still be removable, or the user is pushed back to the rm -rf this
	// command exists to replace
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

// checkIdle refuses the task while any of its rounds is claimed by a run that is still going.
//
// It tries the same lock archive.claimRound holds for a run's lifetime, on every round carrying a marker
// rather than only the ones HasRun accepts: a live run's marker is still empty, so the rounds that matter
// most here are exactly the ones the round enumeration leaves out.
//
// It catches a review that is running now, not one that starts a moment later: a round with no marker yet
// carries nothing to lock, and the lock is released as the check moves on. Both windows are the same size
// and neither is worth closing here — see Cleanup.
//
// The marker is opened through the round's own os.Root, exactly as claimRound opens it, so containment is
// structural rather than a check on a joined path.
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
