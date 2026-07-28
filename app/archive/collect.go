package archive

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/task"
)

// the stage a findings artifact is the output of. stageReport is not a pipeline stage: findings.json is
// what package main wrote after applying --min-confidence, and it takes part in the attrition chain only.
const (
	stageFind      = "find"
	stageSynthesis = "synthesis"
	stageVerify    = "verify"
	stageReport    = "report"
)

// eventAgentRetried is the one events.jsonl entry these numbers read. The vocabulary is spelled here rather
// than imported from app/pipeline: this package reads back what a run wrote, the way History decodes
// findings.json into a record of its own, and pointing the artifact package at the orchestrator is the whole
// cost of one string.
const eventAgentRetried = "agent_retried"

// event is the slice of an events.jsonl line these numbers need. The rest of the line is what makes the file
// large — a findings event carries every finding's body — and none of it is a statistic.
type event struct {
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
}

// namedReport is one findings artifact the round carries, tagged with the stage that produced it.
type namedReport struct {
	stage string
	file  string
	rep   finding.Report
}

// roundStats is one round's contribution to a task: the three tallies taskStats carries, without the id and
// the round count only the fold above it can supply.
type roundStats struct {
	agents []agentStats
	lenses []lensStats
	stages []stageFlow
}

// roundReader accumulates one round's numbers. The round directory is a field rather than a per-call
// argument, so no two methods can disagree about which round they are reading.
type roundReader struct {
	dir string

	agents     map[string]*agentStats
	agentOrder []string
	lenses     map[string]*lensStats
	lensOrder  []string

	// lensSets is each agent's configured lens set, from the snapshot's roster. It is what the ambiguity
	// test compares a finding's lenses against.
	lensSets map[string][]string

	stages []stageFlow
}

func newRoundReader(dir string) *roundReader {
	return &roundReader{
		dir:      dir,
		agents:   map[string]*agentStats{},
		lenses:   map[string]*lensStats{},
		lensSets: map[string][]string{},
	}
}

// read decodes the round's findings artifacts and its event log.
//
// stages/1-found.json is the one that must be there: every Raised is counted from it, and a round without
// one has no review in it to count. The rest are optional — --no-synthesis and --no-verify each write no
// snapshot for the stage they skip — so survivors come from the last stage snapshot the round actually
// carries rather than from a fixed name that may never have been written.
//
// Anything that is there but will not decode is an error, and the caller skips the whole round: an
// interrupted run leaves exactly that, and folding half of one in is worse than leaving it out.
func (r *roundReader) read() error {
	reps, err := r.reports()
	if err != nil {
		return err
	}

	r.roster(reps[0].rep)
	r.addRaised(r.all(reps[0].rep))
	r.addSurvived(r.all(r.lastStage(reps)))
	r.addFlow(reps)
	return r.readEvents()
}

// reports reads the round's findings artifacts in pipeline order, leaving out whichever are not there.
func (r *roundReader) reports() ([]namedReport, error) {
	want := []namedReport{
		{stage: stageFind, file: task.FoundFile},
		{stage: stageSynthesis, file: task.SynthesizedFile},
		{stage: stageVerify, file: task.VerifiedFile},
		{stage: stageReport, file: task.FindingsFile},
	}

	out := make([]namedReport, 0, len(want))
	for _, w := range want {
		rep, ok, err := r.decode(w.file)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		w.rep = rep
		out = append(out, w)
	}
	if len(out) == 0 || out[0].stage != stageFind {
		return nil, fmt.Errorf("round %s carries no %s", r.dir, task.FoundFile)
	}
	return out, nil
}

// decode reads one findings artifact. An absent file is not an error — the round simply never produced that
// one — but an unreadable or malformed one is.
func (r *roundReader) decode(file string) (finding.Report, bool, error) {
	path := filepath.Join(r.dir, file)
	data, err := os.ReadFile(path) //nolint:gosec // a round revmux itself wrote
	if errors.Is(err, fs.ErrNotExist) {
		return finding.Report{}, false, nil
	}
	if err != nil {
		return finding.Report{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var rep finding.Report
	if decErr := json.Unmarshal(data, &rep); decErr != nil {
		return finding.Report{}, false, fmt.Errorf("decode %s: %w", path, decErr)
	}
	return rep, true, nil
}

// roster records the round's source list: which agents ran, what each carried, what each spent, and which
// of them degraded. A stage snapshot is a full finding.Report and carries all of it, so manifest.json is
// not read at all — it holds the same roster and would be a second file to keep in step.
//
// Degraded is SourceStatus.DegradedNames rather than the explicit list alone, since that method exists
// precisely because the two records disagree whenever one is written and the other is not.
func (r *roundReader) roster(rep finding.Report) {
	for _, a := range rep.Sources.Agents {
		r.agent(a.Name).Tokens += a.Tokens
		r.lensSets[a.Name] = a.Lenses
		for _, l := range a.Lenses {
			r.lens(l)
		}
	}
	for _, name := range rep.Sources.DegradedNames() {
		r.agent(name).DegradedRounds++
	}
}

// addRaised counts what each agent and each lens put on the table, from stages/1-found.json.
func (r *roundReader) addRaised(found []finding.Finding) {
	for _, f := range found {
		for _, name := range f.Sources {
			r.agent(name).Raised++
		}
		ambiguous := r.ambiguous(f)
		for _, l := range f.Lenses {
			st := r.lens(l)
			st.Raised++
			if ambiguous {
				st.Ambiguous++
			}
		}
	}
}

// addSurvived counts what came out the far end, from the round's last stage snapshot. Every one of that
// report's four arrays is a survivor: a finding reclassified as an open question, a pre-existing issue or
// an immaterial one still came from the agent that raised it.
func (r *roundReader) addSurvived(survived []finding.Finding) {
	for _, f := range survived {
		for _, name := range f.Sources {
			st := r.agent(name)
			st.Survived++
			if len(f.Sources) > 1 {
				st.Corroborated++
			}
		}
		verdict := f.Verdict
		if verdict == "" {
			verdict = finding.Unverified
		}
		for _, l := range f.Lenses {
			r.lens(l).Verdicts[verdict]++
		}
	}
}

// addFlow records the attrition between consecutive artifacts: how many findings one carried and how many
// the next still did. Only artifacts on disk take part, so a run that skipped a stage shows the transition
// that really happened rather than one inferred from a file nothing ever wrote.
func (r *roundReader) addFlow(reps []namedReport) {
	for i := 1; i < len(reps); i++ {
		r.stages = append(r.stages, stageFlow{
			Name: reps[i].stage,
			In:   len(r.all(reps[i-1].rep)),
			Out:  len(r.all(reps[i].rep)),
		})
	}
}

// readEvents counts the retries the run recorded. An absent events.jsonl is a round that wrote none, not a
// failure, and a last line an interrupted run never finished is skipped rather than fatal.
//
// Lines are read without a size cap: a findings event carries every finding's body, so a fixed scan buffer
// would silently stop counting partway through a large round.
func (r *roundReader) readEvents() error {
	path := filepath.Join(r.dir, task.EventsFile)
	f, err := os.Open(path) //nolint:gosec // a round revmux itself wrote
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	buf := bufio.NewReader(f)
	for {
		line, readErr := buf.ReadString('\n')
		var ev event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == eventAgentRetried && ev.Agent != "" {
			r.agent(ev.Agent).Retries++
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
	}
}

// stats is the round folded into the shape a task accumulates, in roster order rather than map order so two
// reads of one corpus produce the same document.
func (r *roundReader) stats() roundStats {
	out := roundStats{
		agents: make([]agentStats, 0, len(r.agentOrder)),
		lenses: make([]lensStats, 0, len(r.lensOrder)),
		stages: r.stages,
	}
	for _, name := range r.agentOrder {
		out.agents = append(out.agents, *r.agents[name])
	}
	for _, name := range r.lensOrder {
		out.lenses = append(out.lenses, *r.lenses[name])
	}
	return out
}

// lastStage is the report survivors are counted from: the last per-stage snapshot the round carries.
// findings.json is deliberately not one of them — it is what --min-confidence left of the last stage, and
// counting survivors from it read 9 of 17 on a round whose threshold was not zero, which is exactly the
// undercount that would have a reflection agent drop a working agent.
func (r *roundReader) lastStage(reps []namedReport) finding.Report {
	for _, rep := range slices.Backward(reps) {
		if rep.stage != stageReport {
			return rep.rep
		}
	}
	return finding.Report{}
}

// all is every finding a report carries, across its four arrays.
func (r *roundReader) all(rep finding.Report) []finding.Finding {
	out := make([]finding.Finding, 0,
		len(rep.Findings)+len(rep.OpenQuestions)+len(rep.PreExisting)+len(rep.Immaterial))
	out = append(out, rep.Findings...)
	out = append(out, rep.OpenQuestions...)
	out = append(out, rep.PreExisting...)
	return append(out, rep.Immaterial...)
}

// ambiguous reports whether a stage-1 finding's lenses say anything beyond "this agent raised it". The find
// stage falls back to the agent's whole lens set when the model names no valid lens, so a finding carrying
// exactly that set from an agent carrying more than one cannot be attributed to either of them.
func (r *roundReader) ambiguous(f finding.Finding) bool {
	named := slices.Sorted(slices.Values(f.Lenses))
	for _, name := range f.Sources {
		set := r.lensSets[name]
		if len(set) > 1 && slices.Equal(slices.Sorted(slices.Values(set)), named) {
			return true
		}
	}
	return false
}

// agent returns this round's tally for one agent, starting one when the name is new.
func (r *roundReader) agent(name string) *agentStats {
	st, ok := r.agents[name]
	if !ok {
		st = &agentStats{Name: name}
		r.agents[name] = st
		r.agentOrder = append(r.agentOrder, name)
	}
	return st
}

// lens returns this round's tally for one lens, starting one when the name is new.
func (r *roundReader) lens(name string) *lensStats {
	st, ok := r.lenses[name]
	if !ok {
		st = &lensStats{Name: name, Verdicts: map[finding.Verdict]int{}}
		r.lenses[name] = st
		r.lensOrder = append(r.lensOrder, name)
	}
	return st
}
