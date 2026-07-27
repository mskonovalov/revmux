package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/task"
)

const (
	// roundTime is minute precision: the inventory says which round is which, not how long one took.
	roundTime = "2006-01-02T15:04Z"

	unknown = "unknown"
)

// round is one prior round. rep is nil when its findings.json is missing or unparseable, and such a
// round is still listed, with its counts marked unknown — a round that failed badly is evidence too.
type round struct {
	name  string
	mtime time.Time
	rep   *record
}

// record is the slice of a round's findings.json this inventory reads: when the run started, how severe
// each finding was, and how the sources ended up. Nothing prunes rounds, so the number of files decoded
// here grows without bound on the startup path of every review — decoding the full report would parse
// every finding's body, fix and anchor for a line that carries none of them.
type record struct {
	Sources struct {
		Expected int      `json:"expected"`
		Reported int      `json:"reported"`
		Degraded []string `json:"degraded"`
	} `json:"sources"`
	Findings []struct {
		Severity finding.Severity `json:"severity"`
	} `json:"findings"`
	Stats struct {
		StartedAt time.Time `json:"started_at"`
	} `json:"stats"`
}

// History renders the prior-round inventory that every composed prompt carries: the task directory plus
// one line per round with its timestamp, finding counts by severity and source status, so an agent can
// judge which round is worth opening without opening any of them. A task with no prior round yields an
// empty string, which omits the block rather than injecting it empty.
//
// taskDir is the task directory, never a round: the rounds are its children, and pointing this at one of
// them finds nothing and omits the block with no error at all.
//
// Which directories are rounds is task.Rounds, the same enumeration `revmux config` reports: the round
// being written is not one of them yet, so this is resolved before New rather than after it.
//
// It renders no independence instruction. prompt.ComposeOpts appends that to every composed prompt, so
// emitting it here would duplicate it in each one.
func History(taskDir string) (string, error) {
	names, err := task.Rounds(taskDir)
	if err != nil {
		return "", fmt.Errorf("list rounds: %w", err)
	}

	rounds := make([]round, 0, len(names))
	for _, name := range names {
		dir := filepath.Join(taskDir, name)
		fi, statErr := os.Stat(dir)
		if statErr != nil {
			return "", fmt.Errorf("stat %s: %w", dir, statErr)
		}
		rounds = append(rounds, readRound(taskDir, name, fi.ModTime()))
	}
	if len(rounds) == 0 {
		return "", nil
	}

	slices.SortFunc(rounds, func(a, b round) int { return a.order().Compare(b.order()) })
	return renderRounds(taskDir, rounds), nil
}

// readRound reads one round's own record of itself. This is revmux reading what revmux wrote, and it
// carries counts out rather than findings text: the inventory is metadata, not review content.
func readRound(taskDir, name string, mtime time.Time) round {
	r := round{name: name, mtime: mtime}
	data, err := os.ReadFile(filepath.Join(taskDir, name, task.FindingsFile)) //nolint:gosec // a round revmux itself wrote
	if err != nil {
		return r
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return r
	}
	r.rep = &rec
	return r
}

// renderRounds lays the inventory out one round per line, columns padded to the widest entry.
func renderRounds(taskDir string, rounds []round) string {
	var name, at, counts int
	for _, r := range rounds {
		name, at, counts = max(name, len(r.name)), max(at, len(r.at())), max(counts, len(r.counts()))
	}

	out := make([]string, 0, len(rounds)+1)
	out = append(out, "Prior rounds for this task: "+taskDir+string(filepath.Separator))
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

// at is when the round ran, read from its own stats rather than from the directory mtime, which any
// later write into the round rewrites.
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
	if names := r.rep.Sources.Degraded; len(names) > 0 {
		out += ", " + strings.Join(names, ", ") + " degraded"
	}
	return out
}
