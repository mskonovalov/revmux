package archive

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taskName is what every test calls its task directory, since only its containment inside the tasks root
// is ever under test and never the name itself.
const taskName = "pr-123"

func TestNew(t *testing.T) {
	t.Run("creates the run directory under runs/", func(t *testing.T) {
		root, task := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "after-fix", Keep: 10})
		require.NoError(t, err)
		assert.DirExists(t, filepath.Join(task, runsDir, "after-fix"))
		assert.Equal(t, "after-fix", filepath.Base(a.dir))
	})

	t.Run("a name that already exists is an error, never an overwrite", func(t *testing.T) {
		root, task := taskUnder(t)
		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		kept := filepath.Join(task, runsDir, "round-1", "report.md")
		require.NoError(t, os.WriteFile(kept, []byte("the round that went badly"), 0o600))

		_, err = New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "a bad round is exactly the one a reflection agent wants to read")

		data, readErr := os.ReadFile(kept) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "the round that went badly", string(data))
	})

	t.Run("rejects a run name that cannot be one path component", func(t *testing.T) {
		tests := []struct {
			name string
			run  string
			want string
		}{
			{name: "empty", run: "", want: "is empty"},
			{name: "absolute", run: "/etc", want: "is absolute"},
			{name: "separator", run: "a/b", want: "path separator"},
			{name: "parent", run: "..", want: "parent directory"},
			{name: "hidden", run: ".hidden", want: "starts with a dot"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := New(Opts{TasksDir: t.TempDir(), Task: taskName, Run: tt.run})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("no tasks directory", func(t *testing.T) {
		_, err := New(Opts{Task: taskName, Run: "round-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tasks directory is empty")
	})

	t.Run("rejects a task name that cannot be one path component", func(t *testing.T) {
		tests := []struct {
			name string
			task string
			want string
		}{
			{name: "empty", task: "", want: "is empty"},
			{name: "absolute", task: "/etc", want: "is absolute"},
			{name: "separator", task: "a/b", want: "path separator"},
			{name: "parent", task: "..", want: "parent directory"},
			{name: "hidden", task: ".hidden", want: "starts with a dot"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := New(Opts{TasksDir: t.TempDir(), Task: tt.task, Run: "round-1"})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("a task directory that is not there is an error, never one revmux authors", func(t *testing.T) {
		root := t.TempDir()

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "everything above runs/ is the caller's, and he did not write this task")
		assert.Contains(t, err.Error(), "open task directory")
		assert.NoDirExists(t, filepath.Join(root, taskName))
	})

	t.Run("a tasks root that is not there is an error too", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "absent")

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "open tasks directory")
		assert.NoDirExists(t, root)
	})

	t.Run("a symlinked runs directory pointing outside is rejected", func(t *testing.T) {
		root, task := taskUnder(t)
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(task, runsDir)))

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 10})
		require.Error(t, err, "this run would be written there and Prune would delete from there")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.NoDirExists(t, filepath.Join(outside, "round-1"))
	})

	t.Run("a runs symlink landing back inside the task directory is rejected too", func(t *testing.T) {
		root, task := taskUnder(t)
		victim := filepath.Join(task, "context", "prior-notes")
		require.NoError(t, os.MkdirAll(victim, 0o750))
		require.NoError(t, os.Symlink("context", filepath.Join(task, runsDir)))

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 1})
		require.Error(t, err, "containment is satisfied here, so only refusing the link keeps context/ safe")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.DirExists(t, victim, "Prune would have deleted the caller's own context")
	})

	t.Run("a runs directory that already exists is reused, not rejected", func(t *testing.T) {
		root, task := taskUnder(t)
		require.NoError(t, os.MkdirAll(filepath.Join(task, runsDir, "round-1"), 0o750))

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2"})
		require.NoError(t, err, "a task accumulates rounds under one runs/")
		assert.DirExists(t, filepath.Join(task, runsDir, "round-2"))
	})

	t.Run("a symlinked task directory landing inside the tasks root is followed", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real")
		require.NoError(t, os.MkdirAll(target, 0o750))
		require.NoError(t, os.Symlink("real", filepath.Join(root, taskName)))

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err, "a task directory may legitimately be a link, as long as it stays in the root")
		assert.DirExists(t, filepath.Join(target, runsDir, "round-1"))
		assert.Equal(t, "round-1", filepath.Base(a.dir))
	})

	t.Run("a task symlink with an absolute target is refused even inside the tasks root", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real")
		require.NoError(t, os.MkdirAll(target, 0o750))
		require.NoError(t, os.Symlink(target, filepath.Join(root, taskName)))

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "an absolute target cannot be resolved inside the root, so it is not walked")
		assert.NoDirExists(t, filepath.Join(target, runsDir))
	})

	t.Run("a symlinked task directory pointing outside the tasks root is refused", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(root, taskName)))

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "the whole chain is anchored at the tasks root, so this cannot be walked into")
		assert.NoDirExists(t, filepath.Join(outside, runsDir), "nothing of this run was written out there")
	})

	t.Run("a symlinked run directory pointing outside is not adopted", func(t *testing.T) {
		root, task := taskUnder(t)
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(task, runsDir), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(task, runsDir, "evil")))

		_, err := New(Opts{TasksDir: root, Task: taskName, Run: "evil"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create run directory")
	})
}

func TestCheckHandle(t *testing.T) {
	// New looks at an entry, or creates it, and then opens it, and those are two operations: a symlink
	// planted in between is followed by os.Root whenever it lands back inside the parent, which is exactly
	// the runs -> context case the look exists to refuse. These reproduce that window by opening the
	// handle on the swapped entry first and only then running the check that has to catch it.
	t.Run("a symlink swapped in before the open is caught after it", func(t *testing.T) {
		task := t.TempDir()
		victim := filepath.Join(task, "context", "prior-notes")
		require.NoError(t, os.MkdirAll(victim, 0o750))

		taskRoot, err := os.OpenRoot(task)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })

		require.NoError(t, os.Symlink("context", filepath.Join(task, runsDir)))
		runs, err := taskRoot.OpenRoot(runsDir)
		require.NoError(t, err, "containment is satisfied, so the open itself cannot refuse this")
		t.Cleanup(func() { _ = runs.Close() })

		err = checkHandle(taskRoot, runs, filepath.Join(task, runsDir))
		require.Error(t, err, "the handle pins context/, and Prune would delete the caller's own rounds")
		assert.Contains(t, err.Error(), "is a symlink")
	})

	t.Run("a symlink swapped back to a real directory is caught by identity", func(t *testing.T) {
		task := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(task, "context"), 0o750))
		require.NoError(t, os.MkdirAll(filepath.Join(task, "decoy"), 0o750))

		taskRoot, err := os.OpenRoot(task)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })

		require.NoError(t, os.Symlink("context", filepath.Join(task, runsDir)))
		runs, err := taskRoot.OpenRoot(runsDir)
		require.NoError(t, err)
		t.Cleanup(func() { _ = runs.Close() })

		// the link is gone by the time the entry is read again, so only comparing it to the directory
		// actually opened tells the two apart
		require.NoError(t, os.Remove(filepath.Join(task, runsDir)))
		require.NoError(t, os.Rename(filepath.Join(task, "decoy"), filepath.Join(task, runsDir)))

		err = checkHandle(taskRoot, runs, filepath.Join(task, runsDir))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was replaced")
	})

	t.Run("a run directory swapped for a link to an earlier round is caught", func(t *testing.T) {
		runsPath := t.TempDir()
		earlier := filepath.Join(runsPath, "round-1")
		require.NoError(t, os.MkdirAll(earlier, 0o750))

		runs, err := os.OpenRoot(runsPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = runs.Close() })

		// Mkdir created round-2 and refused the name had it been taken; this is the swap after that
		require.NoError(t, runs.Mkdir("round-2", 0o750))
		require.NoError(t, os.Remove(filepath.Join(runsPath, "round-2")))
		require.NoError(t, os.Symlink("round-1", filepath.Join(runsPath, "round-2")))

		run, err := runs.OpenRoot("round-2")
		require.NoError(t, err, "the link lands inside runs/, so the open itself cannot refuse it")
		t.Cleanup(func() { _ = run.Close() })

		err = checkHandle(runs, run, filepath.Join(runsPath, "round-2"))
		require.Error(t, err, "every artifact of this run would truncate one of round-1's")
		assert.Contains(t, err.Error(), "is a symlink")
	})

	t.Run("a run directory renamed away and replaced is caught by identity", func(t *testing.T) {
		runsPath := t.TempDir()
		runs, err := os.OpenRoot(runsPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = runs.Close() })

		require.NoError(t, runs.Mkdir("round-1", 0o750))
		run, err := runs.OpenRoot("round-1")
		require.NoError(t, err)
		t.Cleanup(func() { _ = run.Close() })

		require.NoError(t, os.Rename(filepath.Join(runsPath, "round-1"), filepath.Join(runsPath, "moved")))
		require.NoError(t, os.MkdirAll(filepath.Join(runsPath, "round-1"), 0o750))

		err = checkHandle(runs, run, filepath.Join(runsPath, "round-1"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was replaced")
	})

	t.Run("the real directory New opened passes", func(t *testing.T) {
		task := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(task, runsDir), 0o750))

		taskRoot, err := os.OpenRoot(task)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })
		runs, err := taskRoot.OpenRoot(runsDir)
		require.NoError(t, err)
		t.Cleanup(func() { _ = runs.Close() })

		require.NoError(t, checkHandle(taskRoot, runs, filepath.Join(task, runsDir)))
	})
}

func TestArchive_Close(t *testing.T) {
	t.Run("releases both handles, leaving the artifacts on disk", func(t *testing.T) {
		root, task := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 1})
		require.NoError(t, err)

		w, err := a.Writer("report.md")
		require.NoError(t, err)
		_, err = w.Write([]byte("the report"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		require.NoError(t, a.Close())

		data, readErr := os.ReadFile(filepath.Join(task, runsDir, "round-1", "report.md")) //nolint:gosec // t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "the report", string(data), "Close drops descriptors, never artifacts")
	})

	t.Run("both roots are unusable afterwards", func(t *testing.T) {
		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, a.Close())

		_, err = a.Writer("report.md")
		require.Error(t, err, "the run root is closed")

		require.Error(t, a.Prune(), "the runs root is closed")
	})

	t.Run("closing twice is not an error", func(t *testing.T) {
		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)
		require.NoError(t, a.Close())
		require.NoError(t, a.Close(), "run defers it, so a caller closing early must not fail the run")
	})
}

func TestArchive_Writer(t *testing.T) {
	t.Run("accepts the nested paths every artifact needs", func(t *testing.T) {
		names := []string{
			"events.jsonl",
			"prompts/agents/bugs+impl.md",
			"prompts/stages/verify-app-executor.md",
			"stages/1-found.json",
			"agents/codex.log",
		}

		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				w, err := a.Writer(name)
				require.NoError(t, err)
				_, err = w.Write([]byte("content of " + name))
				require.NoError(t, err)
				require.NoError(t, w.Close())

				data, err := os.ReadFile(filepath.Join(a.dir, filepath.FromSlash(name)))
				require.NoError(t, err)
				assert.Equal(t, "content of "+name, string(data))
			})
		}
	})

	t.Run("a second write to one name replaces rather than appends", func(t *testing.T) {
		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		for _, body := range []string{"first attempt is longer", "second"} {
			w, wErr := a.Writer("stages/1-found.json")
			require.NoError(t, wErr)
			_, wErr = w.Write([]byte(body))
			require.NoError(t, wErr)
			require.NoError(t, w.Close())
		}

		data, err := os.ReadFile(filepath.Join(a.dir, "stages", "1-found.json"))
		require.NoError(t, err)
		assert.Equal(t, "second", string(data))
	})

	t.Run("rejects what escapes the run directory", func(t *testing.T) {
		tests := []struct {
			name     string
			artifact string
			want     string
		}{
			{name: "empty", artifact: "", want: "is empty"},
			{name: "absolute", artifact: "/etc/passwd", want: "is absolute"},
			{name: "parent", artifact: "../../scope.md", want: "escapes"},
			{name: "parent mid-path", artifact: "agents/../../../scope.md", want: "escapes"},
			{name: "the run root itself", artifact: ".", want: "escapes"},
		}

		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := a.Writer(tt.artifact)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("a symlink inside the run defeats the lexical check and is still rejected", func(t *testing.T) {
		root, _ := taskUnder(t)
		outside := t.TempDir()
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)
		require.NoError(t, os.Symlink(outside, filepath.Join(a.dir, "prompts")))

		_, err = a.Writer("prompts/agents/bugs.md")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "escapes")
		assert.NoFileExists(t, filepath.Join(outside, "agents", "bugs.md"))
	})

	t.Run("a runs directory swapped for a symlink mid-run does not redirect a write", func(t *testing.T) {
		root, task := taskUnder(t)
		outside := t.TempDir()
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		// the containment check in New passed on the real directory; this is the swap it cannot see
		require.NoError(t, os.Rename(filepath.Join(task, runsDir), filepath.Join(task, "moved")))
		require.NoError(t, os.Symlink(outside, filepath.Join(task, runsDir)))

		w, err := a.Writer("stages/1-found.json")
		require.NoError(t, err, "the handle still points at the directory New opened")
		_, err = w.Write([]byte("findings"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		assert.NoFileExists(t, filepath.Join(outside, "round-1", "stages", "1-found.json"))
		assert.FileExists(t, filepath.Join(task, "moved", "round-1", "stages", "1-found.json"))
	})

	t.Run("concurrent writers from several agents", func(t *testing.T) {
		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				name := "agents/agent-" + strconv.Itoa(i) + ".jsonl"
				w, wErr := a.Writer(name)
				require.NoError(t, wErr)
				for range 20 {
					_, wErr = w.Write([]byte(`{"type":"system"}` + "\n"))
					require.NoError(t, wErr)
				}
				require.NoError(t, w.Close())
			})
		}
		wg.Wait()

		for i := range 8 {
			data, readErr := os.ReadFile(filepath.Join(a.dir, "agents", "agent-"+strconv.Itoa(i)+".jsonl"))
			require.NoError(t, readErr)
			assert.Len(t, data, 20*len(`{"type":"system"}`+"\n"))
		}
	})

	t.Run("a retried agent keeps both attempts, each parseable on its own", func(t *testing.T) {
		root, _ := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		// the stalled attempt is cut mid-line, which is exactly why it may not share a file with the retry
		attempts := map[string]string{
			"agents/bugs.jsonl":       "{\"type\":\"system\"}\n{\"type\":\"resu",
			"agents/bugs.retry.jsonl": "{\"type\":\"result\"}\n",
		}
		for name, body := range attempts {
			w, wErr := a.Writer(name)
			require.NoError(t, wErr)
			_, wErr = w.Write([]byte(body))
			require.NoError(t, wErr)
			require.NoError(t, w.Close())
		}

		first, err := os.ReadFile(filepath.Join(a.dir, "agents", "bugs.jsonl"))
		require.NoError(t, err)
		assert.Equal(t, attempts["agents/bugs.jsonl"], string(first))

		retry, err := os.ReadFile(filepath.Join(a.dir, "agents", "bugs.retry.jsonl"))
		require.NoError(t, err)
		var ev map[string]string
		require.NoError(t, json.Unmarshal(retry, &ev), "the retry parses on its own, unspliced")
		assert.Equal(t, "result", ev["type"])
	})
}

func TestArchive_Prune(t *testing.T) {
	t.Run("drops the oldest beyond keep", func(t *testing.T) {
		root, task := taskUnder(t)
		olderRun(t, task, "round-1", -3*time.Hour)
		olderRun(t, task, "round-2", -2*time.Hour)
		olderRun(t, task, "round-3", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-4", Keep: 3})
		require.NoError(t, err)
		require.NoError(t, a.Prune())

		assert.NoDirExists(t, filepath.Join(task, runsDir, "round-1"), "the oldest goes first")
		assert.DirExists(t, filepath.Join(task, runsDir, "round-2"))
		assert.DirExists(t, filepath.Join(task, runsDir, "round-3"))
		assert.DirExists(t, filepath.Join(task, runsDir, "round-4"), "the run being written is never a candidate")
	})

	t.Run("keep 0 leaves the current run and every caller-written file", func(t *testing.T) {
		root, task := taskUnder(t)
		context := []string{"scope.md", "goal.md", "profile.md"}
		for _, f := range context {
			require.NoError(t, os.WriteFile(filepath.Join(task, f), []byte("caller content"), 0o600))
		}
		require.NoError(t, os.MkdirAll(filepath.Join(task, "context"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(task, "context", "ticket.md"), []byte("t"), 0o600))
		olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 0})
		require.NoError(t, err)
		require.NoError(t, a.Prune())

		assert.NoDirExists(t, filepath.Join(task, runsDir, "round-1"))
		assert.DirExists(t, filepath.Join(task, runsDir, "round-2"))
		for _, f := range context {
			assert.FileExists(t, filepath.Join(task, f), "pruning never touches what the caller wrote")
		}
		assert.FileExists(t, filepath.Join(task, "context", "ticket.md"))
	})

	t.Run("a no-op when fewer runs exist than keep", func(t *testing.T) {
		root, task := taskUnder(t)
		olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 10})
		require.NoError(t, err)
		require.NoError(t, a.Prune())
		assert.DirExists(t, filepath.Join(task, runsDir, "round-1"))
	})

	t.Run("removes a run holding the whole artifact tree", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)
		for _, sub := range []string{"prompts/agents", "prompts/stages", "stages", "agents"} {
			require.NoError(t, os.MkdirAll(filepath.Join(old, filepath.FromSlash(sub)), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(old, filepath.FromSlash(sub), "f"), []byte("x"), 0o600))
		}

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, a.Prune())
		assert.NoDirExists(t, old)
	})

	t.Run("tolerates an absent runs directory", func(t *testing.T) {
		root, task := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(filepath.Join(task, runsDir)))
		require.NoError(t, a.Prune())
	})

	t.Run("a runs directory swapped for a symlink mid-run is not the one pruned", func(t *testing.T) {
		root, task := taskUnder(t)
		outside := t.TempDir()
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 1})
		require.NoError(t, err)

		// Prune is the one destructive primitive here, so the swap New cannot see must not redirect it
		victim := filepath.Join(outside, "someone-elses-work")
		require.NoError(t, os.MkdirAll(victim, 0o750))
		require.NoError(t, os.Rename(filepath.Join(task, runsDir), filepath.Join(task, "moved")))
		require.NoError(t, os.Symlink(outside, filepath.Join(task, runsDir)))

		require.NoError(t, a.Prune())
		assert.DirExists(t, victim, "the handle enumerates the directory New opened, not what the name now points at")
	})

	t.Run("the run being written survives a rename, since the handle names it and not the caller", func(t *testing.T) {
		root, task := taskUnder(t)
		olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(task, runsDir, "round-2", "report.md"), []byte("r"), 0o600))

		// a name match excludes nothing once the directory has been renamed, and at keep 1 every other
		// entry goes — so this run's own artifacts would be what Prune deleted
		renamed := filepath.Join(task, runsDir, "round-2-moved")
		require.NoError(t, os.Rename(filepath.Join(task, runsDir, "round-2"), renamed))

		require.NoError(t, a.Prune())
		assert.NoDirExists(t, filepath.Join(task, runsDir, "round-1"), "the prior round is still the candidate")
		assert.FileExists(t, filepath.Join(renamed, "report.md"), "this run's own archive is never a candidate")
	})

	t.Run("a candidate renamed away after enumeration is left alone", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(a.dir, "report.md"), []byte("r"), 0o600))

		// what Prune enumerates by identity it deletes by name, so a name that changes hands in between
		// would carry the deletion to whatever answers to it — here, this run's own archive
		entry := runEntry{name: "round-1", info: mustLstat(t, old)}
		require.NoError(t, os.RemoveAll(old))
		require.NoError(t, os.Rename(a.dir, old))

		require.NoError(t, a.remove(entry))
		assert.FileExists(t, filepath.Join(old, "report.md"), "the name leads to this run now, not to the candidate")
	})

	t.Run("a candidate already gone is not an error", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)

		entry := runEntry{name: "round-1", info: mustLstat(t, old)}
		require.NoError(t, os.RemoveAll(old))
		require.NoError(t, a.remove(entry), "someone else deleting it first is the outcome this pass wanted")
	})

	t.Run("a stray file under runs/ is not a run", func(t *testing.T) {
		root, task := taskUnder(t)
		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-1", Keep: 1})
		require.NoError(t, err)

		stray := filepath.Join(task, runsDir, "notes.txt")
		require.NoError(t, os.WriteFile(stray, []byte("x"), 0o600))
		require.NoError(t, a.Prune())
		assert.FileExists(t, stray)
	})
}

func TestArchive_clear(t *testing.T) {
	t.Run("empties the whole tree through the handle, leaving the name only an empty directory", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)
		for _, sub := range []string{"prompts/agents", "prompts/stages", "stages", "agents"} {
			require.NoError(t, os.MkdirAll(filepath.Join(old, filepath.FromSlash(sub)), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(old, filepath.FromSlash(sub), "f"), []byte("x"), 0o600))
		}
		require.NoError(t, os.WriteFile(filepath.Join(old, "report.md"), []byte("r"), 0o600))

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)

		// the recursion is what a redirected name would carry off; only an empty directory is left to it
		require.NoError(t, a.clear(runEntry{name: "round-1", info: mustLstat(t, old)}))
		assert.DirExists(t, old, "clear empties the candidate, the unlink is the caller's next step")
		entries, err := os.ReadDir(old)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("a candidate renamed away before the pin keeps its contents", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(a.dir, "report.md"), []byte("r"), 0o600))

		entry := runEntry{name: "round-1", info: mustLstat(t, old)}
		require.NoError(t, os.RemoveAll(old))
		require.NoError(t, os.Rename(a.dir, old))

		require.NoError(t, a.clear(entry))
		assert.FileExists(t, filepath.Join(old, "report.md"), "the name leads to this run now, not to the candidate")
	})

	t.Run("a symlink answering for the candidate is not followed", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "someone-elses-work"), []byte("x"), 0o600))

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)

		entry := runEntry{name: "round-1", info: mustLstat(t, old)}
		require.NoError(t, os.RemoveAll(old))
		require.NoError(t, os.Symlink(outside, old))

		require.NoError(t, a.clear(entry))
		assert.FileExists(t, filepath.Join(outside, "someone-elses-work"), "a link carries its own identity, not the target's")
	})

	t.Run("a candidate already gone is not an error", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)

		entry := runEntry{name: "round-1", info: mustLstat(t, old)}
		require.NoError(t, os.RemoveAll(old))
		require.NoError(t, a.clear(entry))
	})

	t.Run("a symlink is never the enumerated directory, whatever inode it was handed", func(t *testing.T) {
		root, task := taskUnder(t)
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "someone-elses-work"), []byte("x"), 0o600))

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)

		link := filepath.Join(task, runsDir, "round-1") // New creates runs/, so the link goes in after
		require.NoError(t, os.Symlink(outside, link))

		// the link's OWN identity, so SameFile is necessarily true and cannot be what rejects it.
		// Only the type check can, which is the point: a directory removed and replaced by a link
		// can be handed the inode it just freed, and identity alone then says yes to the link.
		ok, err := a.enumerated(runEntry{name: "round-1", info: mustLstat(t, link)})
		require.NoError(t, err)
		assert.False(t, ok, "a link is not the directory that was enumerated, however its identity reads")
		assert.FileExists(t, filepath.Join(outside, "someone-elses-work"))
	})

	t.Run("a symlink inside the candidate is unlinked rather than followed", func(t *testing.T) {
		root, task := taskUnder(t)
		old := olderRun(t, task, "round-1", -time.Hour)
		outside := t.TempDir()
		victim := filepath.Join(outside, "context")
		require.NoError(t, os.MkdirAll(victim, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(victim, "ticket.md"), []byte("t"), 0o600))
		require.NoError(t, os.Symlink(victim, filepath.Join(old, "agents")))

		a, err := New(Opts{TasksDir: root, Task: taskName, Run: "round-2", Keep: 1})
		require.NoError(t, err)

		require.NoError(t, a.clear(runEntry{name: "round-1", info: mustLstat(t, old)}))
		assert.NoFileExists(t, filepath.Join(old, "agents"), "the link itself goes")
		assert.FileExists(t, filepath.Join(victim, "ticket.md"), "what it pointed at does not")
	})
}

// taskUnder makes one task directory under its own tasks root, returning both: New is anchored at the
// root and names the task, while the assertions still need the directory the artifacts land in.
func taskUnder(t *testing.T) (root, task string) {
	t.Helper()
	root = t.TempDir()
	task = filepath.Join(root, taskName)
	require.NoError(t, os.MkdirAll(task, 0o750))
	return root, task
}

// mustLstat reads the identity Prune records for a candidate, which is what remove matches the name
// against before deleting anything.
func mustLstat(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	return fi
}

// olderRun writes one prior round with a backdated mtime, which is what Prune orders by.
func olderRun(t *testing.T, task, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(task, runsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	at := time.Now().Add(age)
	require.NoError(t, os.Chtimes(dir, at, at))
	return dir
}
