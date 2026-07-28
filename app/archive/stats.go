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
type stageFlow struct {
	Name string `json:"name"`
	In   int    `json:"in"`
	Out  int    `json:"out"`
}

// StatsQuery selects what CollectStats reads. Two adjacent string parameters would be a swap hazard, and
// swapping these silently reads the wrong directory.
type StatsQuery struct {
	TasksDir string
	Task     string // empty means every task under TasksDir
}
