package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
)

// thinGroup is the size below which a directory does not earn its own verifier and is merged with
// the other thin ones. One finding is not worth a process of its own.
const thinGroup = 2

// labelDirs caps how many directory names a merged group spells out before the rest are counted,
// since task 11 turns the label into a filename.
const labelDirs = 3

// labelUnsafe is everything a group label may not carry. Separators collapse to a dash rather than
// being dropped, so app/executor and appexecutor cannot label the same.
var labelUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// verifier owns the verify stage: one agent per directory group, each seeing only its own findings.
// Materiality is a per-claim judgment, and a verifier holding the whole list anchors on the first
// few and then rubber-stamps or batch-rejects the rest.
//
// The stagger is handed in rather than constructed: the gate latched open during find, so a fresh
// instance would charge this stage another stagger delay to re-prove auth find already proved.
type verifier struct {
	cfg     Config
	emit    func(Event)
	save    func(name string, data []byte)
	stagger *stagger

	stage  *prompt.Stage // resolved once in run, before any group goroutine reads it
	tokens atomic.Int64
}

// verifyGroup is what one verifier sees: the directories it covers, their findings and the prompt
// composed from them. The composed text rides along because task 11 archives it per group, under a
// filename built from the same label.
type verifyGroup struct {
	dirs     []string
	findings []finding.Finding
	text     string
}

// verdict is the wire shape of one entry the model returns. The correction fields apply to a
// refined verdict only, and a zero one leaves the original value alone.
type verdict struct {
	ID         string           `json:"id"`
	Verdict    finding.Verdict  `json:"verdict"`
	Line       int              `json:"line"`
	EndLine    int              `json:"end_line"`
	Severity   finding.Severity `json:"severity"`
	Confidence int              `json:"confidence"`
	Title      string           `json:"title"`
	Body       string           `json:"body"`
	Fix        string           `json:"fix"`
}

// run verifies every finding and routes it by the verdict it came back with. Rejected findings are
// dropped; immaterial and pre-existing move to their own lists, which is why the stage returns a
// whole report rather than a slice.
func (v *verifier) run(ctx context.Context, rep finding.Report) (finding.Report, error) {
	groups := v.groupByDir(rep.Findings)
	if len(groups) == 0 {
		return rep, nil
	}

	if err := v.compose(groups); err != nil {
		return finding.Report{}, err
	}

	judged := make([][]finding.Finding, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Go(func() { judged[i] = v.judge(ctx, g, i+1) })
	}
	wg.Wait()

	rep.Findings = nil
	for _, list := range judged {
		for _, f := range list {
			switch f.Verdict {
			case finding.Immaterial:
				rep.Immaterial = append(rep.Immaterial, f)
			case finding.PreExisting:
				rep.PreExisting = append(rep.PreExisting, f)
			default:
				rep.Findings = append(rep.Findings, f)
			}
		}
	}
	rep.Stats.Tokens += int(v.tokens.Load())
	return rep, nil
}

// compose builds every group's prompt before any of them is dispatched. A prompt tree that does not
// compose is a config error every group would hit identically, so it fails the run rather than
// degrading each group in turn — an unresolved variable is a bug, not a warning.
func (v *verifier) compose(groups []verifyGroup) error {
	stage, err := v.cfg.Set.Stage(stageVerify)
	if err != nil {
		return fmt.Errorf("resolve verify stage: %w", err)
	}
	v.stage = stage

	for i := range groups {
		text, err := stage.Compose(prompt.ComposeOpts{Vars: v.vars(groups[i]), History: v.cfg.History})
		if err != nil {
			return fmt.Errorf("compose verify prompt for %s: %w", groups[i].label(), err)
		}
		groups[i].text = text
		// one file per group: this stage fans out per directory, so a single verify.md would lose
		// every prompt but the last and leave "what did that verifier see" unanswerable
		v.save(v.promptName(groups[i]), []byte(text))
	}
	return nil
}

// promptName is where one group's composed prompt goes. The label is already a filename-safe slug, so
// a group covering app/executor lands in a file rather than a stray nested directory.
func (v *verifier) promptName(g verifyGroup) string {
	return path.Join(stagePromptDir, stageVerify+"-"+g.label()+".md")
}

// judge runs one group and falls back to leaving its findings unverified when the verifier does not
// deliver. A dead verifier must not discard a review find and synthesis already paid for, and an
// unverified verdict says exactly what happened rather than claiming the finding was checked.
//
// index is never zero: the leader slot belongs to the find stage, and a group taking it would open
// a gate that is already open.
func (v *verifier) judge(ctx context.Context, g verifyGroup, index int) []finding.Finding {
	if err := v.stagger.acquire(ctx, index); err != nil {
		return v.unverifiedGroup(g, err)
	}
	defer v.stagger.release()

	out, err := v.runOne(ctx, g)
	if err != nil {
		return v.unverifiedGroup(g, err)
	}
	return out
}

// runOne runs one group's already-composed prompt and applies the verdicts it returned.
func (v *verifier) runOne(ctx context.Context, g verifyGroup) ([]finding.Finding, error) {
	agent := stageVerify + "-" + g.label()
	v.emit(Event{Kind: EventAgentStarted, Agent: agent, Text: strings.Join(g.dirs, ", ")})

	spec := RunnerSpec{Executor: v.stage.Executor, Model: v.stage.Model, Effort: v.stage.Effort}
	res, err := v.cfg.NewRunner(spec).Run(ctx, executor.Request{
		Prompt: g.text, Model: v.stage.Model, Effort: v.stage.Effort, Schema: finding.VerifySchema(),
	}, newSink(agent, v.emit, nil))
	if err != nil {
		return nil, fmt.Errorf("verify %s: %w", g.label(), err)
	}
	v.tokens.Add(int64(res.Tokens))

	out, err := v.apply(g, res.StructuredOutput)
	if err != nil {
		return nil, err
	}
	v.emit(Event{Kind: EventAgentDone, Agent: agent, Text: strconv.Itoa(len(out)) + " of " +
		strconv.Itoa(len(g.findings)) + " kept"})
	return out, nil
}

// apply turns the returned verdicts into findings. A finding the model said nothing about stays,
// marked unverified: silence is not a rejection, and dropping it would let a lazy answer delete a
// real problem.
func (v *verifier) apply(g verifyGroup, raw json.RawMessage) ([]finding.Finding, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("verify %s returned no structured output", g.label())
	}

	var out struct {
		Verdicts []verdict `json:"verdicts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode verdicts for %s: %w", g.label(), err)
	}

	byID := make(map[string]verdict, len(out.Verdicts))
	for _, d := range out.Verdicts {
		byID[d.ID] = d
	}

	kept := make([]finding.Finding, 0, len(g.findings))
	for _, f := range g.findings {
		d, ok := byID[f.ID]
		if !ok || !d.known() {
			f.Verdict = finding.Unverified
			kept = append(kept, f)
			continue
		}
		if d.Verdict == finding.Rejected {
			continue
		}
		kept = append(kept, d.applyTo(f))
	}
	return kept, nil
}

// vars adds this group's findings to the run's own. A verifier sees only its own group, so nothing
// here carries a finding from another one.
func (v *verifier) vars(g verifyGroup) prompt.Vars {
	out := prompt.Vars{}
	maps.Copy(out, v.cfg.Vars)
	out["FINDINGS"] = g.findingsBlock()
	return out
}

// groupByDir buckets findings by directory, merges the thin buckets into one and caps how many
// groups result. Directory approximates code locality, so one verifier reads that area once and
// judges several findings against it instead of re-reading a file per finding.
func (v *verifier) groupByDir(findings []finding.Finding) []verifyGroup {
	byDir := map[string][]finding.Finding{}
	for _, f := range findings {
		dir := v.dir(f)
		byDir[dir] = append(byDir[dir], f)
	}

	groups, thin := []verifyGroup{}, verifyGroup{}
	for _, dir := range slices.Sorted(maps.Keys(byDir)) {
		g := verifyGroup{dirs: []string{dir}, findings: byDir[dir]}
		if len(g.findings) < thinGroup {
			thin = thin.merge(g)
			continue
		}
		groups = append(groups, g)
	}
	if len(thin.findings) > 0 {
		groups = append(groups, thin)
	}
	return v.capped(groups)
}

// capped merges the smallest groups together until the count fits the configured limit. How many
// verifiers run is a spend decision; which findings each one judges is not, so the merge takes
// whole directories rather than splitting one.
func (v *verifier) capped(groups []verifyGroup) []verifyGroup {
	limit := v.cfg.VerifyGroups
	if limit <= 0 || len(groups) <= limit {
		return groups
	}

	ordered := slices.Clone(groups)
	slices.SortStableFunc(ordered, func(a, b verifyGroup) int { return len(a.findings) - len(b.findings) })

	merged := verifyGroup{}
	for _, g := range ordered[:len(ordered)-limit+1] {
		merged = merged.merge(g)
	}
	out := append([]verifyGroup{merged}, ordered[len(ordered)-limit+1:]...)
	slices.SortFunc(out, func(a, b verifyGroup) int { return strings.Compare(a.label(), b.label()) })
	return out
}

// dir is the bucket one finding falls in. A file-level finding carries no line and a repository-root
// file has no directory, and both land in the same root bucket.
func (v *verifier) dir(f finding.Finding) string {
	if f.File == "" {
		return "."
	}
	return path.Dir(f.File)
}

// unverifiedGroup marks a group nothing judged and says so on the event channel, so a failed
// verifier is visible rather than looking like a group that confirmed everything.
func (v *verifier) unverifiedGroup(g verifyGroup, err error) []finding.Finding {
	v.emit(Event{Kind: EventAgentDegraded, Agent: stageVerify + "-" + g.label(), Text: err.Error()})
	out := make([]finding.Finding, 0, len(g.findings))
	for _, f := range g.findings {
		f.Verdict = finding.Unverified
		out = append(out, f)
	}
	return out
}

// unverified is the --no-verify path: every finding is marked unverified rather than shipping with
// an empty verdict that reads like it was checked. Open questions and pre-existing issues are never
// verified, so neither is touched.
func (v *verifier) unverified(rep finding.Report) finding.Report {
	for i := range rep.Findings {
		rep.Findings[i].Verdict = finding.Unverified
	}
	return rep
}

// known reports whether the model answered with a verdict from the enum. The codex path has no
// schema to enforce one, so an unrecognized word must not reach the report as if it were a judgment.
func (d verdict) known() bool {
	switch d.Verdict {
	case finding.Confirmed, finding.Refined, finding.Rejected, finding.Immaterial, finding.PreExisting:
		return true
	default:
		return false
	}
}

// applyTo folds one verdict into the finding it judged. Only a refined verdict carries corrections,
// and an omitted field leaves the original value in place.
func (d verdict) applyTo(f finding.Finding) finding.Finding {
	f.Verdict = d.Verdict
	if d.Verdict != finding.Refined {
		return f
	}
	if d.Line > 0 {
		f.Line = d.Line
	}
	if d.EndLine > 0 {
		f.EndLine = d.EndLine
	}
	if d.Severity != "" {
		f.Severity = d.Severity
	}
	if d.Confidence > 0 {
		f.Confidence = d.Confidence
	}
	if d.Title != "" {
		f.Title = d.Title
	}
	if d.Body != "" {
		f.Body = d.Body
	}
	if d.Fix != "" {
		f.Fix = d.Fix
	}
	return f
}

// merge folds another group into this one. It copies rather than appending in place, since the
// findings slices come from the grouping map and must not be shared between two groups.
func (g verifyGroup) merge(o verifyGroup) verifyGroup {
	return verifyGroup{dirs: slices.Concat(g.dirs, o.dirs), findings: slices.Concat(g.findings, o.findings)}
}

// label is the group's filename-safe slug: task 11 builds prompts/stages/verify-<label>.md from it,
// so a separator here would silently create a nested directory instead of a file.
func (g verifyGroup) label() string {
	if len(g.dirs) == 0 {
		return "root"
	}

	named := g.dirs
	suffix := ""
	if len(named) > labelDirs {
		named, suffix = named[:labelDirs], "+"+strconv.Itoa(len(g.dirs)-labelDirs)+"-more"
	}
	parts := make([]string, 0, len(named))
	for _, d := range named {
		parts = append(parts, g.slug(d))
	}
	return strings.Join(parts, "+") + suffix
}

func (g verifyGroup) slug(dir string) string {
	out := strings.Trim(labelUnsafe.ReplaceAllString(dir, "-"), "-.")
	if out == "" {
		return "root"
	}
	return out
}

// findingsBlock is what the group's verifier is asked to judge, ids included, since a verdict names
// the finding it applies to.
func (g verifyGroup) findingsBlock() string {
	b, _ := json.MarshalIndent(g.findings, "", "  ") // Finding is plain data, so encoding cannot fail
	return string(b)
}
