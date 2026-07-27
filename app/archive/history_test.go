package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

// testNow is the fixed reference every round in these tests is stamped against. Ordering compares a
// round's own started_at against another's directory mtime, so mixing a fixed timestamp with a
// time.Now() offset makes the expected order depend on when the suite runs.
var testNow = time.Date(2026, 7, 26, 14, 30, 0, 0, time.UTC)

func roundAt(offset time.Duration) time.Time { return testNow.Add(offset) }

func TestHistory(t *testing.T) {
	t.Run("no prior round omits the block entirely", func(t *testing.T) {
		got, err := History(t.TempDir())
		require.NoError(t, err)
		assert.Empty(t, got, "an empty inventory would inject a block saying nothing")
	})

	t.Run("a task nobody has run yet omits the block too", func(t *testing.T) {
		got, err := History(filepath.Join(t.TempDir(), "pr-123"))
		require.NoError(t, err, "the task directory is written by the caller, and a review of its first round precedes it")
		assert.Empty(t, got)
	})

	t.Run("what a round is comes from the manifest, not from being a directory", func(t *testing.T) {
		taskDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(taskDir, "notes"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.md"), []byte("---\nurl: x\n---\n"), 0o600))

		// the marker a run that never came back leaves: the round is re-runnable, so listing it would put
		// the round being written into its own inventory
		claim := filepath.Join(taskDir, "01-initial")
		require.NoError(t, os.MkdirAll(claim, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(claim, manifestFile), nil, 0o600))

		got, err := History(taskDir)
		require.NoError(t, err)
		assert.Empty(t, got, "a directory a caller left here never ran, and task.md is not a round at all")
	})

	t.Run("rounds are listed oldest first with counts and source status", func(t *testing.T) {
		taskDir := t.TempDir()
		// written newest first, so ordering cannot come from directory iteration order
		writeRound(t, taskDir, "after-fix", finding.Report{
			Sources: finding.SourceStatus{Expected: 4, Reported: 3, DegradedSources: []string{"docs+tests"}},
			Findings: []finding.Finding{
				{Severity: finding.Major, Title: "one"},
				{Severity: finding.Minor, Title: "two"},
			},
			Stats: finding.Stats{StartedAt: time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)},
		})
		writeRound(t, taskDir, "round-1", finding.Report{
			Sources: finding.SourceStatus{Expected: 4, Reported: 4},
			Findings: []finding.Finding{
				{Severity: finding.Critical, Title: "a"},
				{Severity: finding.Major, Title: "b"},
				{Severity: finding.Major, Title: "c"},
				{Severity: finding.Minor, Title: "d"},
			},
			Stats: finding.Stats{StartedAt: testNow},
		})

		got, err := History(taskDir)
		require.NoError(t, err)

		lines := strings.Split(got, "\n")
		require.Len(t, lines, 3)
		assert.Equal(t, "Prior rounds for this task: "+taskDir+string(filepath.Separator), lines[0])

		assert.Contains(t, lines[1], "round-1")
		assert.Contains(t, lines[1], "2026-07-26T14:30Z")
		assert.Contains(t, lines[1], "4 findings (1 critical, 2 major, 1 minor)")
		assert.Contains(t, lines[1], "sources 4/4")
		assert.NotContains(t, lines[1], "degraded")

		assert.Contains(t, lines[2], "after-fix")
		assert.Contains(t, lines[2], "2026-07-26T16:02Z")
		assert.Contains(t, lines[2], "2 findings (0 critical, 1 major, 1 minor)")
		assert.Contains(t, lines[2], "sources 3/4, docs+tests degraded")
	})

	t.Run("the inventory carries no findings text and no independence guard", func(t *testing.T) {
		taskDir := t.TempDir()
		writeRound(t, taskDir, "round-1", finding.Report{
			Findings: []finding.Finding{{Severity: finding.Major, Title: "the leak nobody fixed", Body: "details"}},
			Stats:    finding.Stats{StartedAt: testNow},
		})

		got, err := History(taskDir)
		require.NoError(t, err)
		assert.NotContains(t, got, "the leak nobody fixed", "the inventory is metadata, findings stay in the files")
		assert.NotContains(t, got, "Re-evaluate", "the guard is appended by the prompt composer, never duplicated here")
	})

	t.Run("a round with no readable findings.json is listed with its counts unknown", func(t *testing.T) {
		taskDir := t.TempDir()
		writeRound(t, taskDir, "good", finding.Report{
			Sources: finding.SourceStatus{Expected: 1, Reported: 1},
			Stats:   finding.Stats{StartedAt: testNow},
		})

		// both fall back to mtime, so they are stamped against the same fixed clock as good above:
		// a wall-clock offset would order them ahead of good once the real time passes 14:30Z
		absent := claimedRound(t, taskDir, "never-finished")
		require.NoError(t, os.Chtimes(absent, roundAt(-2*time.Hour), roundAt(-2*time.Hour)))

		broken := claimedRound(t, taskDir, "half-written")
		require.NoError(t, os.WriteFile(filepath.Join(broken, findingsFile), []byte(`{"findings":`), 0o600))
		// backdated after the write, since writing into a directory is what moves its mtime
		require.NoError(t, os.Chtimes(broken, roundAt(-3*time.Hour), roundAt(-3*time.Hour)))

		got, err := History(taskDir)
		require.NoError(t, err)

		lines := strings.Split(got, "\n")
		require.Len(t, lines, 4, "a round that failed badly is still evidence")
		assert.Contains(t, lines[1], "half-written")
		assert.Contains(t, lines[1], "counts unknown")
		assert.Contains(t, lines[1], "sources unknown")
		assert.Contains(t, lines[2], "never-finished")
		assert.Contains(t, lines[2], unknown, "no timestamp is honest, a directory mtime is not the round's own")
		assert.Contains(t, lines[3], "good")
		assert.DirExists(t, absent)
	})

	t.Run("a round whose stats carry no start time falls back to its place on disk", func(t *testing.T) {
		taskDir := t.TempDir()
		writeRound(t, taskDir, "stamped", finding.Report{
			Stats: finding.Stats{StartedAt: testNow},
		})
		writeRound(t, taskDir, "unstamped", finding.Report{})
		dir := filepath.Join(taskDir, "unstamped")
		at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, os.Chtimes(dir, at, at))

		got, err := History(taskDir)
		require.NoError(t, err)
		lines := strings.Split(got, "\n")
		require.Len(t, lines, 3)
		assert.Contains(t, lines[1], "unstamped")
		assert.Contains(t, lines[1], unknown)
		assert.Contains(t, lines[2], "stamped")
	})

	t.Run("an unreadable task directory is an error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		taskDir := filepath.Join(t.TempDir(), "pr-123")
		require.NoError(t, os.MkdirAll(taskDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(taskDir, 0o750) }) //nolint:gosec // restores the temp dir so cleanup can remove it
		_, err := History(taskDir)
		require.Error(t, err)
	})
}

// writeRound stores one prior round the way a finished run leaves it: a direct child of the task
// directory carrying the manifest that marks it a round and the findings.json History reads.
func writeRound(t *testing.T, taskDir, name string, rep finding.Report) {
	t.Helper()
	dir := claimedRound(t, taskDir, name)

	f, err := os.Create(filepath.Join(dir, findingsFile)) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, rep.JSON(f))
	require.NoError(t, f.Close())
}

// claimedRound makes one round directory holding only the manifest a finished run left in it, which is
// what tells a round apart from any other directory under the task — including one whose run never
// came back and left the marker empty.
func claimedRound(t *testing.T, taskDir, name string) string {
	t.Helper()
	dir := filepath.Join(taskDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), []byte("{}"), 0o600))
	return dir
}
