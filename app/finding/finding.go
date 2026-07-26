// Package finding holds the review data contract: a single Finding, the Report carrying
// findings out of the pipeline, and the JSON schema each stage asks its model for.
package finding

import (
	"strconv"
	"strings"
)

// Severity is how bad a finding is.
type Severity string

// Severity values, most severe first.
const (
	Critical Severity = "critical"
	Major    Severity = "major"
	Minor    Severity = "minor"
)

// Verdict is what the verification stage decided about a finding. Unverified is set by
// revmux when the stage is skipped, the rest come from the model.
type Verdict string

// Verdict values.
const (
	Confirmed   Verdict = "confirmed"
	Refined     Verdict = "refined"
	Rejected    Verdict = "rejected"
	Immaterial  Verdict = "immaterial"
	PreExisting Verdict = "pre_existing"
	Unverified  Verdict = "unverified"
)

// Finding is one reported problem. Sources holds agent names and is the only input to the
// cross-source confidence boost; Lenses holds the lens names that raised it and is informational.
// The two are never interchangeable: a source is a process, so an agent carrying two lenses that
// flags an issue under both is still one source.
type Finding struct {
	ID         string   `json:"id"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	EndLine    int      `json:"end_line"`
	Severity   Severity `json:"severity"`
	Confidence int      `json:"confidence"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Fix        string   `json:"fix"`
	Sources    []string `json:"sources"`
	Lenses     []string `json:"lenses"`
	Verdict    Verdict  `json:"verdict"`
}

// Location renders the three shapes a finding anchor can take: a file with no line at all,
// a single line, and a range.
func (f Finding) Location() string {
	switch {
	case f.File == "":
		return "(no file)"
	case f.Line <= 0:
		return f.File
	case f.EndLine > f.Line:
		return f.File + ":" + strconv.Itoa(f.Line) + "-" + strconv.Itoa(f.EndLine)
	}
	return f.File + ":" + strconv.Itoa(f.Line)
}

func (f Finding) markdownLines() []string {
	out := []string{"### " + f.Title, "", "`" + f.Location() + "`", ""}
	if f.Body != "" {
		out = append(out, f.Body, "")
	}
	if f.Fix != "" {
		out = append(out, "Fix: "+f.Fix, "")
	}
	return append(out, f.metaLine(), "")
}

func (f Finding) metaLine() string {
	parts := []string{"confidence: " + strconv.Itoa(f.Confidence)}
	if len(f.Sources) > 0 {
		parts = append(parts, "sources: "+strings.Join(f.Sources, ", "))
	}
	if len(f.Lenses) > 0 {
		parts = append(parts, "lenses: "+strings.Join(f.Lenses, ", "))
	}
	if f.Verdict != "" {
		parts = append(parts, "verdict: "+string(f.Verdict))
	}
	return "_" + strings.Join(parts, " | ") + "_"
}

// rank orders severities for report grouping; anything unrecognized sorts last rather than
// being dropped, so a typo in a model's answer stays visible.
func (s Severity) rank() int {
	switch s {
	case Critical:
		return 0
	case Major:
		return 1
	case Minor:
		return 2
	}
	return 3
}

func (s Severity) heading() string {
	if s == "" {
		return "Unspecified"
	}
	return strings.ToUpper(string(s[:1])) + string(s[1:])
}
