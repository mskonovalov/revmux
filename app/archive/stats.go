package archive

import "github.com/umputun/revmux/app/finding"

// Corpus is every task's review record under one tasks root: what each agent and each lens produced across
// the rounds that ran, and how many findings the pipeline dropped between stages.
//
// It is read back out of what revmux itself wrote, so every number below names the artifact it came from:
// the same count is a different number depending on which one it was taken from, and a reflection agent
// acting on the wrong one drops a working agent.
type Corpus struct {
	Tasks  []taskStats `json:"tasks"`
	Totals taskStats   `json:"totals"`
}

// taskStats is one task's rounds folded together. ID is empty on Corpus.Totals, which is every task at once.
type taskStats struct {
	ID string `json:"id,omitempty"`

	// Description is what this task's task.md says it covers, from the same task.Load `revmux config`
	// reports it with. It is here so one call answers what a caller deciding between tasks needs — an id
	// alone says nothing about what would be given up by removing it. Empty on Corpus.Totals, and empty
	// for a task with no task.md or one that would not parse: this command reports the record, and
	// `revmux config` is where a parse failure is named.
	Description string `json:"description,omitempty"`

	// SizeMB is what this task occupies on disk, every round and the caller's own input/ alike, summed
	// from the file sizes rather than from block counts so two filesystems report the same task the same.
	// It is the only number here that is not read back out of an artifact's content.
	SizeMB float64 `json:"size_mb"`

	// sizeBytes is what SizeMB is rounded from, kept so the totals fold exact byte counts: adding the
	// rounded megabytes of every task drifts by up to a tenth each, against a threshold the user set.
	sizeBytes int64

	// LastRun is the finished_at of the most recent round's manifest, as a date. The manifest is written
	// when the run completes, so this is when the task was last reviewed rather than when anything last
	// touched the directory. Empty when no round carries one.
	LastRun string `json:"last_run,omitempty"`

	// Rounds is the rounds these numbers were read from: task.HasRun accepts it, so a prepared round nobody
	// ran is not one, and its artifacts decoded, so a round left half-written by an interrupted run is not
	// one either. It is the denominator of everything beside it.
	Rounds int `json:"rounds"`

	// Skipped is one entry per round left out of Rounds because its artifacts would not decode, each naming
	// the artifact and why. A count that shrank silently reads as a corpus that is simply smaller, which is
	// the same wrong advice `revmux config` refuses to give by reporting rounds_error rather than no rounds.
	Skipped []string `json:"skipped"`

	Agents []agentStats `json:"agents"`
	Lenses []lensStats  `json:"lenses"`
	Stages []stageFlow  `json:"stages"`
}

// agentStats is what one roster entry produced.
type agentStats struct {
	// Name is the agent's roster name, read from sources.agents[] of a stage snapshot.
	Name string `json:"name"`

	// Raised is findings in stages/1-found.json naming this agent in sources — what it put on the table
	// before synthesis merged anything.
	Raised int `json:"raised"`

	// Survived is findings naming this agent in the round's last stage snapshot, counted across all four of
	// that report's arrays: a finding reclassified as an open question or a pre-existing issue still came
	// from this agent. It is stages/3-verified.json for a full pipeline and the stage before it for a run
	// that skipped verification — down to stages/1-found.json itself for one that skipped both, where
	// nothing filtered anything and Survived therefore equals Raised. findings.json is never read for it:
	// that is the --min-confidence-filtered report.
	Survived int `json:"survived"`

	// Corroborated is the Survived subset whose sources names more than one agent, so another process
	// independently reached the same finding.
	Corroborated int `json:"corroborated"`

	// DegradedRounds is rounds whose stage snapshot names this agent in SourceStatus.DegradedNames, which
	// merges the explicit degraded list with the per-agent flags because the two disagree.
	DegradedRounds int `json:"degraded_rounds"`

	// Retries is agent_retried entries in events.jsonl naming this agent. No stage snapshot records a
	// relaunch, so the event log is the only artifact that carries this.
	Retries int `json:"retries"`

	// Tokens is summed from sources.agents[].tokens of a stage snapshot.
	Tokens int `json:"tokens"`
}

// lensStats is what one lens raised and what became of it. A per-lens number is only as good as its
// Ambiguous share, so the two belong together wherever either is quoted.
type lensStats struct {
	// Name is the lens name as it appears in a finding's lenses array.
	Name string `json:"name"`

	// Raised is findings in stages/1-found.json naming this lens. Stage 1 is the only place the number
	// means anything: after synthesis a finding's lenses is a union across merged findings from different
	// agents, so it no longer says which lens saw what.
	Raised int `json:"raised"`

	// Ambiguous is the Raised subset whose finding named exactly its agent's whole lens set while that agent
	// carried more than one lens — the shape the find stage produces when the model named no valid lens and
	// it fell back to the full set. It over-counts a model that genuinely named every lens it carried;
	// nothing in the archive tells the two apart.
	Ambiguous int `json:"ambiguous"`

	// Verdicts counts this lens's surviving findings by verdict, from the same stage snapshot Survived is
	// read from. A finding that never went through verification — a pre-existing issue, or a run with
	// --no-verify — carries no verdict and counts as unverified, which is what unverified means.
	Verdicts map[finding.Verdict]int `json:"verdicts"`
}

// stageFlow is how many findings went into one stage and how many came out. Name is the stage that produced
// Out; the find stage has no entry, since nothing goes into it. Each count is the union of a report's four
// finding arrays, so the chain is comparable end to end.
//
// **In and Out alone understate verification, and Reclassified and Refined are here because of it.** The
// counts are that union, so a finding verification moved out of the actionable list — into immaterial or
// pre-existing — leaves the total unchanged and shows as no attrition at all. Measured over one corpus,
// verify dropped 2 findings of the 150 that reached it while lowering the severity of 21: read on In and
// Out alone it looks like a stage doing nothing, which is the case for removing it, and the case is wrong.
type stageFlow struct {
	Name string `json:"name"`
	In   int    `json:"in"`
	Out  int    `json:"out"`

	// Reclassified is findings this stage moved into the immaterial or pre-existing arrays: real, and
	// judged either not worth fixing or not this change's. They leave the actionable list without leaving
	// the report, so they are invisible in Out.
	Reclassified int `json:"reclassified,omitempty"`

	// Refined is findings this stage kept and rewrote — the verdict verification gives a finding whose
	// substance held but whose framing, location or severity did not.
	Refined int `json:"refined,omitempty"`
}

// StatsQuery selects what CollectStats reads. Two adjacent string parameters would be a swap hazard, and
// swapping these silently reads the wrong directory.
type StatsQuery struct {
	TasksDir string
	Task     string // empty means every task under TasksDir
}

// CleanupResult is what Cleanup removed and what the tasks root costs afterwards. Removed is an array
// holding the one task this call took, so a caller reading it need not special-case the single-task shape
// against the numbers `revmux stats` reports as arrays.
type CleanupResult struct {
	TasksDir string    `json:"tasks_dir"`
	Removed  []Removal `json:"removed"`

	// TotalMB is absent when the root could not be measured after the removal. That measurement happens
	// after the tree is gone, so its failure may not fail the call — the removal succeeded, and a caller
	// told otherwise reports a task that is in fact removed. An omitted number says only that: what went
	// is still in Removed, measured before it went.
	TotalMB *float64 `json:"total_mb_after,omitempty"`
}

// Removal is one task that is gone, measured before it went: after the tree is removed there is nothing
// left to read these back from, and they are the caller's only record of what he gave up.
type Removal struct {
	ID     string  `json:"id"`
	Rounds int     `json:"rounds"`
	SizeMB float64 `json:"size_mb"`
}
