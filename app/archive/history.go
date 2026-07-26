package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/umputun/revmux/app/finding"
)

const (
	findingsFile = "findings.json"

	// roundTime is minute precision: the inventory says which round is which, not how long one took.
	roundTime = "2006-01-02T15:04Z"

	unknown = "unknown"
)

// round is one prior round. rep is nil when its findings.json is missing or unparseable, and such a
// round is still listed, with its counts marked unknown — a round that failed badly is evidence too.
type round struct {
	name  string
	mtime time.Time
	rep   *finding.Report
}

// History renders the prior-round inventory that every composed prompt carries: the runs/ path plus
// one line per round with its timestamp, finding counts by severity and source status, so an agent can
// judge which round is worth opening without opening any of them. A task with no prior round yields an
// empty string, which omits the block rather than injecting it empty.
//
// It renders no independence instruction. prompt.ComposeOpts appends that to every composed prompt, so
// emitting it here would duplicate it in each one.
//
// Call it before Prune: pruning drops the oldest rounds, and a round read afterwards is one that no
// longer exists.
func History(taskDir string) (string, error) {
	root := filepath.Join(taskDir, runsDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}

	rounds := make([]round, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			return "", fmt.Errorf("stat %s: %w", filepath.Join(root, e.Name()), infoErr)
		}
		rounds = append(rounds, readRound(root, e.Name(), info.ModTime()))
	}
	if len(rounds) == 0 {
		return "", nil
	}

	slices.SortFunc(rounds, func(a, b round) int { return a.order().Compare(b.order()) })
	return renderRounds(root, rounds), nil
}

// readRound reads one round's own record of itself. This is revmux reading what revmux wrote, and it
// carries counts out rather than findings text: the inventory is metadata, not review content.
func readRound(root, name string, mtime time.Time) round {
	r := round{name: name, mtime: mtime}
	data, err := os.ReadFile(filepath.Join(root, name, findingsFile)) //nolint:gosec // a run directory revmux itself wrote
	if err != nil {
		return r
	}
	var rep finding.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return r
	}
	r.rep = &rep
	return r
}

// renderRounds lays the inventory out one round per line, columns padded to the widest entry.
func renderRounds(root string, rounds []round) string {
	var name, at, counts int
	for _, r := range rounds {
		name, at, counts = max(name, len(r.name)), max(at, len(r.at())), max(counts, len(r.counts()))
	}

	out := make([]string, 0, len(rounds)+1)
	out = append(out, "Prior rounds for this task: "+root+string(filepath.Separator))
	for _, r := range rounds {
		out = append(out, fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
			name, r.name, at, r.at(), counts, r.counts(), r.sources()))
	}
	return strings.Join(out, "\n")
}

// order is the sort key. A round's own started_at is authoritative; the directory mtime is the
// fallback for one whose findings.json could not be read, since it still has a place in the sequence.
func (r round) order() time.Time {
	if r.rep != nil && !r.rep.Stats.StartedAt.IsZero() {
		return r.rep.Stats.StartedAt
	}
	return r.mtime
}

// at is when the round ran, read from its own stats rather than from the directory mtime, which both
// pruning and copying rewrite.
func (r round) at() string {
	if r.rep == nil || r.rep.Stats.StartedAt.IsZero() {
		return unknown
	}
	return r.rep.Stats.StartedAt.UTC().Format(roundTime)
}

func (r round) counts() string {
	if r.rep == nil {
		return "counts " + unknown
	}
	by := map[finding.Severity]int{}
	for _, f := range r.rep.Findings {
		by[f.Severity]++
	}
	return fmt.Sprintf("%d findings (%d critical, %d major, %d minor)",
		len(r.rep.Findings), by[finding.Critical], by[finding.Major], by[finding.Minor])
}

func (r round) sources() string {
	if r.rep == nil {
		return "sources " + unknown
	}
	out := fmt.Sprintf("sources %d/%d", r.rep.Sources.Reported, r.rep.Sources.Expected)
	if names := r.rep.Sources.DegradedSources; len(names) > 0 {
		out += ", " + strings.Join(names, ", ") + " degraded"
	}
	return out
}
