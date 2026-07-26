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

func TestHistory(t *testing.T) {
	t.Run("no prior round omits the block entirely", func(t *testing.T) {
		got, err := History(t.TempDir())
		require.NoError(t, err)
		assert.Empty(t, got, "an empty inventory would inject a block saying nothing")
	})

	t.Run("an empty runs directory is still no history", func(t *testing.T) {
		task := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(task, runsDir), 0o750))
		got, err := History(task)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("rounds are listed oldest first with counts and source status", func(t *testing.T) {
		task := t.TempDir()
		// written newest first, so ordering cannot come from directory iteration order
		writeRound(t, task, "after-fix", finding.Report{
			Sources: finding.SourceStatus{Expected: 4, Reported: 3, DegradedSources: []string{"docs+tests"}},
			Findings: []finding.Finding{
				{Severity: finding.Major, Title: "one"},
				{Severity: finding.Minor, Title: "two"},
			},
			Stats: finding.Stats{StartedAt: time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)},
		})
		writeRound(t, task, "round-1", finding.Report{
			Sources: finding.SourceStatus{Expected: 4, Reported: 4},
			Findings: []finding.Finding{
				{Severity: finding.Critical, Title: "a"},
				{Severity: finding.Major, Title: "b"},
				{Severity: finding.Major, Title: "c"},
				{Severity: finding.Minor, Title: "d"},
			},
			Stats: finding.Stats{StartedAt: time.Date(2026, 7, 26, 14, 30, 0, 0, time.UTC)},
		})

		got, err := History(task)
		require.NoError(t, err)

		lines := strings.Split(got, "\n")
		require.Len(t, lines, 3)
		assert.Equal(t, "Prior rounds for this task: "+filepath.Join(task, runsDir)+string(filepath.Separator), lines[0])

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
		task := t.TempDir()
		writeRound(t, task, "round-1", finding.Report{
			Findings: []finding.Finding{{Severity: finding.Major, Title: "the leak nobody fixed", Body: "details"}},
			Stats:    finding.Stats{StartedAt: time.Date(2026, 7, 26, 14, 30, 0, 0, time.UTC)},
		})

		got, err := History(task)
		require.NoError(t, err)
		assert.NotContains(t, got, "the leak nobody fixed", "the inventory is metadata, findings stay in the files")
		assert.NotContains(t, got, "Re-evaluate", "the guard is appended by the prompt composer, never duplicated here")
	})

	t.Run("a round with no readable findings.json is listed with its counts unknown", func(t *testing.T) {
		task := t.TempDir()
		writeRound(t, task, "good", finding.Report{
			Sources: finding.SourceStatus{Expected: 1, Reported: 1},
			Stats:   finding.Stats{StartedAt: time.Date(2026, 7, 26, 14, 30, 0, 0, time.UTC)},
		})

		absent := olderRun(t, task, "never-finished", -2*time.Hour)
		broken := filepath.Join(task, runsDir, "half-written")
		require.NoError(t, os.MkdirAll(broken, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(broken, findingsFile), []byte(`{"findings":`), 0o600))
		// backdated after the write, since writing into a directory is what moves its mtime
		at := time.Now().Add(-3 * time.Hour)
		require.NoError(t, os.Chtimes(broken, at, at))

		got, err := History(task)
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
		task := t.TempDir()
		writeRound(t, task, "stamped", finding.Report{
			Stats: finding.Stats{StartedAt: time.Date(2026, 7, 26, 14, 30, 0, 0, time.UTC)},
		})
		writeRound(t, task, "unstamped", finding.Report{})
		dir := filepath.Join(task, runsDir, "unstamped")
		at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, os.Chtimes(dir, at, at))

		got, err := History(task)
		require.NoError(t, err)
		lines := strings.Split(got, "\n")
		require.Len(t, lines, 3)
		assert.Contains(t, lines[1], "unstamped")
		assert.Contains(t, lines[1], unknown)
		assert.Contains(t, lines[2], "stamped")
	})

	t.Run("an unreadable runs directory is an error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		task := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(task, runsDir), 0o000))
		_, err := History(task)
		require.Error(t, err)
	})
}

// writeRound stores one prior round the way a finished run leaves it: its own findings.json under
// runs/<name>/, which is the only thing History reads.
func writeRound(t *testing.T, task, name string, rep finding.Report) {
	t.Helper()
	dir := filepath.Join(task, runsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o750))

	f, err := os.Create(filepath.Join(dir, findingsFile)) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, rep.JSON(f))
	require.NoError(t, f.Close())
}
