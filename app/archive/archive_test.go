package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("creates the run directory under runs/", func(t *testing.T) {
		task := t.TempDir()
		a, err := New(Opts{TaskDir: task, Run: "after-fix", Keep: 10})
		require.NoError(t, err)
		assert.DirExists(t, filepath.Join(task, runsDir, "after-fix"))
		assert.Equal(t, "after-fix", filepath.Base(a.dir))
	})

	t.Run("a name that already exists is an error, never an overwrite", func(t *testing.T) {
		task := t.TempDir()
		_, err := New(Opts{TaskDir: task, Run: "round-1"})
		require.NoError(t, err)

		kept := filepath.Join(task, runsDir, "round-1", "report.md")
		require.NoError(t, os.WriteFile(kept, []byte("the round that went badly"), 0o600))

		_, err = New(Opts{TaskDir: task, Run: "round-1"})
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
				_, err := New(Opts{TaskDir: t.TempDir(), Run: tt.run})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("no task directory", func(t *testing.T) {
		_, err := New(Opts{Run: "round-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "task directory is empty")
	})

	t.Run("a symlinked run directory pointing outside is not adopted", func(t *testing.T) {
		task, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(task, runsDir), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(task, runsDir, "evil")))

		_, err := New(Opts{TaskDir: task, Run: "evil"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create run directory")
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

		a, err := New(Opts{TaskDir: t.TempDir(), Run: "round-1"})
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
		a, err := New(Opts{TaskDir: t.TempDir(), Run: "round-1"})
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

		a, err := New(Opts{TaskDir: t.TempDir(), Run: "round-1"})
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
		task, outside := t.TempDir(), t.TempDir()
		a, err := New(Opts{TaskDir: task, Run: "round-1"})
		require.NoError(t, err)
		require.NoError(t, os.Symlink(outside, filepath.Join(a.dir, "prompts")))

		_, err = a.Writer("prompts/agents/bugs.md")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside")
		assert.NoFileExists(t, filepath.Join(outside, "agents", "bugs.md"))
	})

	t.Run("concurrent writers from several agents", func(t *testing.T) {
		a, err := New(Opts{TaskDir: t.TempDir(), Run: "round-1"})
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
		a, err := New(Opts{TaskDir: t.TempDir(), Run: "round-1"})
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
		task := t.TempDir()
		olderRun(t, task, "round-1", -3*time.Hour)
		olderRun(t, task, "round-2", -2*time.Hour)
		olderRun(t, task, "round-3", -time.Hour)

		a, err := New(Opts{TaskDir: task, Run: "round-4", Keep: 3})
		require.NoError(t, err)
		require.NoError(t, a.Prune())

		assert.NoDirExists(t, filepath.Join(task, runsDir, "round-1"), "the oldest goes first")
		assert.DirExists(t, filepath.Join(task, runsDir, "round-2"))
		assert.DirExists(t, filepath.Join(task, runsDir, "round-3"))
		assert.DirExists(t, filepath.Join(task, runsDir, "round-4"), "the run being written is never a candidate")
	})

	t.Run("keep 0 leaves the current run and every caller-written file", func(t *testing.T) {
		task := t.TempDir()
		context := []string{"scope.md", "goal.md", "profile.md"}
		for _, f := range context {
			require.NoError(t, os.WriteFile(filepath.Join(task, f), []byte("caller content"), 0o600))
		}
		require.NoError(t, os.MkdirAll(filepath.Join(task, "context"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(task, "context", "ticket.md"), []byte("t"), 0o600))
		olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TaskDir: task, Run: "round-2", Keep: 0})
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
		task := t.TempDir()
		olderRun(t, task, "round-1", -time.Hour)

		a, err := New(Opts{TaskDir: task, Run: "round-2", Keep: 10})
		require.NoError(t, err)
		require.NoError(t, a.Prune())
		assert.DirExists(t, filepath.Join(task, runsDir, "round-1"))
	})

	t.Run("removes a run holding the whole artifact tree", func(t *testing.T) {
		task := t.TempDir()
		old := olderRun(t, task, "round-1", -time.Hour)
		for _, sub := range []string{"prompts/agents", "prompts/stages", "stages", "agents"} {
			require.NoError(t, os.MkdirAll(filepath.Join(old, filepath.FromSlash(sub)), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(old, filepath.FromSlash(sub), "f"), []byte("x"), 0o600))
		}

		a, err := New(Opts{TaskDir: task, Run: "round-2", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, a.Prune())
		assert.NoDirExists(t, old)
	})

	t.Run("tolerates an absent runs directory", func(t *testing.T) {
		task := t.TempDir()
		a, err := New(Opts{TaskDir: task, Run: "round-1", Keep: 1})
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(filepath.Join(task, runsDir)))
		require.NoError(t, a.Prune())
	})

	t.Run("a stray file under runs/ is not a run", func(t *testing.T) {
		task := t.TempDir()
		a, err := New(Opts{TaskDir: task, Run: "round-1", Keep: 1})
		require.NoError(t, err)

		stray := filepath.Join(task, runsDir, "notes.txt")
		require.NoError(t, os.WriteFile(stray, []byte("x"), 0o600))
		require.NoError(t, a.Prune())
		assert.FileExists(t, stray)
	})
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
