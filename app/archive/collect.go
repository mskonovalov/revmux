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
	"strings"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/task"
)

// CollectStats aggregates every round that ran under the query's tasks root: one entry per task, plus the
// totals across all of them.
//
// Which directories are tasks is task.List and which are rounds is task.Rounds, the same two enumerations
// `revmux config` reports. Two walks over the tasks root are two chances to disagree about what a task is,
// and a caller reading both commands would see that disagreement as a task that has no history.
//
// A round whose artifacts will not decode is left out rather than folded in half — an interrupted run
// leaves exactly that — but an unreadable task directory is an error: reported as no rounds it reads as a
// task nobody reviewed, which is the wrong advice this whole document exists to avoid giving.
func CollectStats(q StatsQuery) (Corpus, error) {
	ids, err := q.tasks()
	if err != nil {
		return Corpus{}, err
	}

	out := Corpus{Tasks: make([]taskStats, 0, len(ids)), Totals: newTaskStats("")}
	for _, id := range ids {
		st, collectErr := q.collect(id)
		if collectErr != nil {
			return Corpus{}, collectErr
		}
		out.Tasks = append(out.Tasks, st)
		out.Totals.add(st)
	}
	return out, nil
}

// tasks names the tasks this query covers, narrowed to q.Task when it is set. The narrowed name is looked
// up in the enumeration rather than joined into a path of its own, so a name that is not one task directory
// under the root — a separator, a parent hop, a link the archive could not walk — matches nothing and is
// reported as absent.
//
// A tasks root that is not there has no tasks and is not an error: a project that has never run a review is
// not a failure. Asking for one task by name is different — a typo'd id answered with an empty document
// reads as a task with no history, so it says so instead.
func (q StatsQuery) tasks() ([]string, error) {
	ids, err := task.List(q.TasksDir)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	if q.Task == "" {
		return ids, nil
	}
	if !slices.Contains(ids, q.Task) {
		return nil, fmt.Errorf("task %s not found under %s", q.Task, q.TasksDir)
	}
	return []string{q.Task}, nil
}

// collect folds one task's rounds into a single tally. Rounds counts the rounds these numbers were read
// from, so a round skipped for being unreadable is not one of them: it is the denominator of everything
// beside it.
//
// A skipped round is named in Skipped rather than dropped without a word. Three rounds where five ran is a
// smaller corpus reading as a whole one, and the numbers a reflection agent then acts on are exactly the
// ones that shrank.
func (q StatsQuery) collect(id string) (taskStats, error) {
	dir := filepath.Join(q.TasksDir, id)
	rounds, err := task.Rounds(dir)
	if err != nil {
		return taskStats{}, fmt.Errorf("list rounds of task %s: %w", id, err)
	}

	out := newTaskStats(id)
	for _, name := range rounds {
		r := newRoundReader(filepath.Join(dir, name))
		if readErr := r.read(); readErr != nil {
			out.Skipped = append(out.Skipped, readErr.Error())
			continue
		}
		st := r.stats()
		out.add(taskStats{Rounds: 1, Agents: st.agents, Lenses: st.lenses, Stages: st.stages})
	}

	// what this task costs and when it was last reviewed are not read from a round's findings, so they are
	// filled after the fold rather than accumulated through it: a round skipped for unreadable artifacts
	// still occupies the disk it occupies, and leaving it out would understate exactly the task a caller
	// deciding what to reclaim is looking at
	size, unread, err := dirSize(dir)
	if err != nil {
		return taskStats{}, err
	}
	out.sizeBytes, out.SizeMB, out.LastRun = size, mb(size), lastRun(dir, rounds)
	// an entry the walk could not read leaves the size a floor, and that is said rather than left to be
	// read as the task's real cost — one unreadable round under one task must not discard the corpus
	for _, path := range unread {
		out.Skipped = append(out.Skipped, path+": unreadable, so size_mb is a floor")
	}

	// a task.md that is absent or will not parse leaves the description empty rather than failing the
	// call: `revmux config` is where a parse failure is named, and a corpus is not the place to learn of one
	if meta, metaErr := task.Load(dir); metaErr == nil {
		out.Description = meta.Description
	}
	return out, nil
}

// newTaskStats starts an empty tally. The slices are non-nil because the caller is a program reading this
// back: a task with no rounds yet marshals as empty arrays rather than as nulls it has to special-case.
func newTaskStats(id string) taskStats {
	return taskStats{ID: id, Skipped: []string{},
		Agents: []agentStats{}, Lenses: []lensStats{}, Stages: []stageFlow{}}
}

// add folds another tally into this one, matching agents, lenses and stages by name and appending a name
// this one has not seen. Two tasks with different rosters therefore keep every entry of both.
//
// It accumulates into the receiver rather than merging two arguments side by side: with two taskStats as
// parameters the argument order silently decides which ID and Rounds survive, and both readings compile.
func (t *taskStats) add(o taskStats) {
	t.Rounds += o.Rounds
	// bytes rather than the reported megabytes: folding rounded values would drift by a tenth per task,
	// and the totals are what a caller compares against the threshold he set
	t.sizeBytes += o.sizeBytes
	t.SizeMB = mb(t.sizeBytes)
	// dates are ISO, so the later one is the greater one; the totals' date is the corpus's own last review
	if o.LastRun > t.LastRun {
		t.LastRun = o.LastRun
	}
	t.Skipped = append(t.Skipped, o.Skipped...)
	t.addAgents(o.Agents)
	t.addLenses(o.Lenses)
	t.addStages(o.Stages)
}

func (t *taskStats) addAgents(agents []agentStats) {
	idx := make(map[string]int, len(t.Agents))
	for i, a := range t.Agents {
		idx[a.Name] = i
	}
	for _, a := range agents {
		i, ok := idx[a.Name]
		if !ok {
			i = len(t.Agents)
			idx[a.Name] = i
			t.Agents = append(t.Agents, agentStats{Name: a.Name})
		}
		st := &t.Agents[i]
		st.Raised += a.Raised
		st.Survived += a.Survived
		st.Corroborated += a.Corroborated
		st.DegradedRounds += a.DegradedRounds
		st.Retries += a.Retries
		st.Tokens += a.Tokens
	}
}

// addLenses folds the per-lens tallies, starting a fresh verdict map for a lens this tally has not seen.
// Taking the other side's map would alias it, and a task entry and the totals sharing one map means the
// next task's verdicts land in both.
func (t *taskStats) addLenses(lenses []lensStats) {
	idx := make(map[string]int, len(t.Lenses))
	for i, l := range t.Lenses {
		idx[l.Name] = i
	}
	for _, l := range lenses {
		i, ok := idx[l.Name]
		if !ok {
			i = len(t.Lenses)
			idx[l.Name] = i
			t.Lenses = append(t.Lenses, lensStats{Name: l.Name, Verdicts: map[finding.Verdict]int{}})
		}
		st := &t.Lenses[i]
		st.Raised += l.Raised
		st.Ambiguous += l.Ambiguous
		for verdict, n := range l.Verdicts {
			st.Verdicts[verdict] += n
		}
	}
}

func (t *taskStats) addStages(stages []stageFlow) {
	idx := make(map[string]int, len(t.Stages))
	for i, s := range t.Stages {
		idx[s.Name] = i
	}
	for _, s := range stages {
		i, ok := idx[s.Name]
		if !ok {
			i = len(t.Stages)
			idx[s.Name] = i
			t.Stages = append(t.Stages, stageFlow{Name: s.Name})
		}
		st := &t.Stages[i]
		st.In += s.In
		st.Out += s.Out
	}
}

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
			In:   r.count(reps[i-1].rep),
			Out:  r.count(reps[i].rep),
		})
	}
}

// count is how many findings a report carries across its four arrays. The flow needs the number alone, and
// materializing the findings to take len of them is four copies of every finding per stage transition.
func (r *roundReader) count(rep finding.Report) int {
	return len(rep.Findings) + len(rep.OpenQuestions) + len(rep.PreExisting) + len(rep.Immaterial)
}

// readEvents counts the retries the run recorded. An absent events.jsonl is a round that wrote none, not a
// failure, and a last line an interrupted run never finished is skipped rather than fatal.
//
// Only a name this round already tallied is counted — the roster the snapshot recorded, plus the sources
// its findings were stamped with, which find fills with the executing agent's own name. The stages retry
// under the same event kind, the synthesis stage emitting one that names itself, and a name nothing ran
// under would otherwise become a source no roster contains, reported beside the agents that really ran.
//
// Lines are read without a size cap: a findings event carries every finding's body, so a fixed scan buffer
// would silently stop counting partway through a large round. That is also why the kind is looked for in
// the raw line first — every other event is discarded, and a findings event carries every finding's body
// through the decoder to reach a field two of a thousand lines can match.
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
		// the substring is the cheap filter and the decode is the real test: a findings event quoting the
		// kind gets past the first and is turned away by the second
		if strings.Contains(line, eventAgentRetried) {
			var ev event
			if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == eventAgentRetried {
				if st, ok := r.agents[ev.Agent]; ok {
					st.Retries++
				}
			}
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
	out := make([]finding.Finding, 0, r.count(rep))
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
