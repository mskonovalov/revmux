package prompt

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	executorClaude = "claude"

	// overrideAgent is the name the --lenses override's single agent carries. It reaches
	// Finding.sources and becomes agents/<name>.jsonl, so it can never be empty.
	overrideAgent = "lenses"

	// ansiDefaultFg restores the default foreground and nothing else. A full reset would clear the
	// enclosing pane's background too.
	ansiDefaultFg = "\x1b[39m"
)

// the two accepted vocabularies, checked by validate and reported verbatim by `revmux config`.
var (
	executors = []string{executorClaude, "codex"}
	efforts   = []string{"low", "medium", "high", "xhigh", "max"}
)

// ansiColors maps the accepted color names to their ANSI index. A raw index is deliberately not
// accepted as front matter: `color: 12` says nothing to whoever edits the file, and the name always
// exists. An index is what resolution produces, drawn from the reader's own terminal theme.
var ansiColors = map[string]int{
	"black": 0, "red": 1, "green": 2, "yellow": 3,
	"blue": 4, "magenta": 5, "cyan": 6, "white": 7,
	"bright-black": 8, "bright-red": 9, "bright-green": 10, "bright-yellow": 11,
	"bright-blue": 12, "bright-magenta": 13, "bright-cyan": 14, "bright-white": 15,
}

// colorPalette fills an omitted color by roster position rather than by hashing the name, so two
// runs of one profile color an agent identically even after the reviewer renames it.
var colorPalette = []string{"cyan", "magenta", "green", "yellow", "blue", "red", "bright-cyan", "bright-magenta"}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Each kind of prompt file declares its own front-matter shape rather than sharing one, because
// unknown keys are rejected and one shared shape accepts every key in every file. A profile-level
// executor: reads exactly like the model: and effort: defaults beside it, would parse, and would then
// be ignored while every agent ran on claude — the silent-default this package rejects everywhere else.

// profileYAML is a profile's front matter: roster-wide model and effort defaults, plus the roster.
// Which binary runs an entry is per-entry only, since a roster mixing claude and codex is the point.
type profileYAML struct {
	Description string      `yaml:"description"`
	Model       string      `yaml:"model"`
	Effort      string      `yaml:"effort"`
	Agents      []agentYAML `yaml:"agents"`
}

// stageYAML is a stage prompt's front matter. A stage picks its own binary, model and effort exactly
// as a roster entry does, and has no roster of its own.
type stageYAML struct {
	Description string `yaml:"description"`
	Executor    string `yaml:"executor"`
	Model       string `yaml:"model"`
	Effort      string `yaml:"effort"`
}

// lensYAML is a lens file's front matter. A lens is executor-agnostic text and carries no runner
// selection: the roster entry composing it is where model, effort and executor live.
type lensYAML struct {
	Description string `yaml:"description"`
}

type agentYAML struct {
	Name     string   `yaml:"name"`
	Lenses   []string `yaml:"lenses"`
	Executor string   `yaml:"executor"`
	Model    string   `yaml:"model"`
	Effort   string   `yaml:"effort"`
	Color    string   `yaml:"color"`
}

// Profile is a roster plus the shared body every agent in it composes.
type Profile struct {
	doc
	Name   string
	model  string
	effort string
	agents []AgentSpec
}

// Stage is a stage prompt — synthesis or verify. It has no roster and must not expose one, but it
// selects its own binary, model and effort exactly as a roster entry does.
type Stage struct {
	doc
	Name     string
	Executor string
	Model    string
	Effort   string
}

// AgentSpec is one roster entry. Color is the resolved form a renderer hands straight to lipgloss —
// an ANSI index "0"-"15" or the original #RRGGBB — and ColorName is the authored value, empty when
// the color came from the palette, so `revmux config` reads back `cyan` rather than "6".
type AgentSpec struct {
	Name      string   `json:"name"`
	Lenses    []string `json:"lenses"`
	Executor  string   `json:"executor"`
	Model     string   `json:"model,omitempty"`
	Effort    string   `json:"effort,omitempty"`
	Color     string   `json:"color"`
	ColorName string   `json:"color_name,omitempty"`
}

// Efforts returns the accepted effort vocabulary, the same slice validate checks against.
func Efforts() []string { return slices.Clone(efforts) }

// Executors returns the accepted executor vocabulary, the same slice validate checks against.
func Executors() []string { return slices.Clone(executors) }

// Roster returns the resolved roster, every entry carrying a color. A non-empty lensOverride
// replaces the roster with a single agent carrying every named lens — one viewpoint, not two
// corroborating votes — inheriting the profile's model and effort and running on claude, so a
// roster's codex entry does not survive it.
func (p *Profile) Roster(lensOverride []string, known map[string]struct{}) ([]AgentSpec, error) {
	specs := slices.Clone(p.agents)
	if len(lensOverride) > 0 {
		override := AgentSpec{
			Name: overrideAgent, Lenses: slices.Clone(lensOverride),
			Executor: executorClaude, Model: p.model, Effort: p.effort,
		}
		if err := override.validate(known); err != nil {
			return nil, fmt.Errorf("lens override: %w", err)
		}
		specs = []AgentSpec{override}
	}

	for i := range specs {
		specs[i].Lenses = slices.Clone(specs[i].Lenses)
		color, err := specs[i].resolveColor()
		if err != nil {
			return nil, err
		}
		if color == "" {
			color = strconv.Itoa(ansiColors[colorPalette[i%len(colorPalette)]])
		}
		specs[i].Color = color
	}
	return specs, nil
}

func (p *Profile) validate(known map[string]struct{}) error {
	if len(p.agents) == 0 {
		return fmt.Errorf("profile %s: roster is empty", p.Name)
	}
	seen := make(map[string]struct{}, len(p.agents))
	for _, a := range p.agents {
		if err := a.validate(known); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("profile %s: duplicate agent name %q", p.Name, a.Name)
		}
		seen[a.Name] = struct{}{}
	}
	return nil
}

func (st *Stage) validate() error {
	if !slices.Contains(executors, st.Executor) {
		return fmt.Errorf("stage %s: unknown executor %q, want one of %v", st.Name, st.Executor, executors)
	}
	if st.Effort != "" && !slices.Contains(efforts, st.Effort) {
		return fmt.Errorf("stage %s: unknown effort %q, want one of %v", st.Name, st.Effort, efforts)
	}
	return nil
}

func (a AgentSpec) validate(known map[string]struct{}) error {
	if err := a.checkName(); err != nil {
		return err
	}
	if len(a.Lenses) == 0 {
		return fmt.Errorf("agent %s: no lenses", a.Name)
	}
	for _, l := range a.Lenses {
		if _, ok := known[l]; !ok {
			return fmt.Errorf("agent %s: unknown lens %q", a.Name, l)
		}
	}
	if !slices.Contains(executors, a.Executor) {
		return fmt.Errorf("agent %s: unknown executor %q, want one of %v", a.Name, a.Executor, executors)
	}
	if a.Effort != "" && !slices.Contains(efforts, a.Effort) {
		return fmt.Errorf("agent %s: unknown effort %q, want one of %v", a.Name, a.Effort, efforts)
	}
	if _, err := a.resolveColor(); err != nil {
		return fmt.Errorf("agent %s: %w", a.Name, err)
	}
	return nil
}

// checkName rejects a name that cannot become one path component. The archive turns it into
// agents/<name>.jsonl and prompts/agents/<name>.md, so a separator or a parent reference would let a
// profile write outside the run directory — the same rule package main applies to --task and --run.
func (a AgentSpec) checkName() error {
	switch {
	case a.Name == "":
		return errors.New("roster entry has no name")
	case filepath.IsAbs(a.Name):
		return fmt.Errorf("agent %q is an absolute path", a.Name)
	case strings.ContainsAny(a.Name, `/\`):
		return fmt.Errorf("agent %q contains a path separator", a.Name)
	case strings.Contains(a.Name, ".."):
		return fmt.Errorf("agent %q references a parent directory", a.Name)
	case strings.HasPrefix(a.Name, "."):
		return fmt.Errorf("agent %q starts with a dot", a.Name)
	}
	return nil
}

// Paint wraps s in the agent's resolved color. Both renderers call it, so the TUI and the plain
// --no-tui output show one agent in one color; an entry with no resolved color is left alone.
//
// It emits raw SGR rather than going through lipgloss: a nested lipgloss render ends in a full reset
// that kills the enclosing pane's background, and its color profile is read from stdout, which is not
// where either renderer writes.
func (a AgentSpec) Paint(s string) string {
	seq := a.sgr()
	if seq == "" || s == "" {
		return s
	}
	return seq + s + ansiDefaultFg
}

// sgr renders the resolved color as a foreground sequence. An index picks the color out of the
// reader's own terminal theme; a hex value asks for that exact shade and ignores the theme.
func (a AgentSpec) sgr() string {
	if strings.HasPrefix(a.Color, "#") {
		rgb, err := strconv.ParseUint(a.Color[1:], 16, 32)
		if err != nil {
			return ""
		}
		return "\x1b[38;2;" + strconv.FormatUint(rgb>>16&0xff, 10) + ";" +
			strconv.FormatUint(rgb>>8&0xff, 10) + ";" + strconv.FormatUint(rgb&0xff, 10) + "m"
	}
	idx, err := strconv.Atoi(a.Color)
	if err != nil || idx < 0 || idx > 15 {
		return ""
	}
	if idx < 8 {
		return "\x1b[3" + strconv.Itoa(idx) + "m"
	}
	return "\x1b[9" + strconv.Itoa(idx-8) + "m"
}

func (a AgentSpec) resolveColor() (string, error) {
	if a.ColorName == "" {
		return "", nil
	}
	if idx, ok := ansiColors[a.ColorName]; ok {
		return strconv.Itoa(idx), nil
	}
	if hexColor.MatchString(a.ColorName) {
		return a.ColorName, nil
	}
	return "", fmt.Errorf("invalid color %q, want an ANSI-16 name or #RRGGBB", a.ColorName)
}

func parseProfile(name string, meta, body []byte) (*Profile, error) {
	var fm profileYAML
	text, err := parseFile(meta, body, &fm)
	if err != nil {
		return nil, err
	}

	p := &Profile{doc: doc{Description: fm.Description, Body: text}, Name: name, model: fm.Model, effort: fm.Effort}
	p.agents = make([]AgentSpec, 0, len(fm.Agents))
	for _, a := range fm.Agents {
		spec := AgentSpec{
			Name: a.Name, Lenses: a.Lenses, Executor: a.Executor,
			Model: a.Model, Effort: a.Effort, ColorName: a.Color,
		}
		if spec.Executor == "" {
			spec.Executor = executorClaude
		}
		if spec.Model == "" {
			spec.Model = fm.Model
		}
		if spec.Effort == "" {
			spec.Effort = fm.Effort
		}
		p.agents = append(p.agents, spec)
	}
	return p, nil
}

func parseStage(name string, meta, body []byte) (*Stage, error) {
	var fm stageYAML
	text, err := parseFile(meta, body, &fm)
	if err != nil {
		return nil, err
	}

	st := &Stage{doc: doc{Description: fm.Description, Body: text}, Name: name,
		Executor: fm.Executor, Model: fm.Model, Effort: fm.Effort}
	if st.Executor == "" {
		st.Executor = executorClaude
	}
	return st, nil
}

func parseLens(meta, body []byte) (doc, error) {
	var fm lensYAML
	text, err := parseFile(meta, body, &fm)
	if err != nil {
		return doc{}, err
	}
	return doc{Description: fm.Description, Body: text}, nil
}
