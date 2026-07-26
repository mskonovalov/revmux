package prompt

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

const (
	executorClaude = "claude"

	// overrideAgent is the name the --lenses override's single agent carries. It reaches
	// Finding.sources and becomes agents/<name>.jsonl, so it can never be empty.
	overrideAgent = "lenses"
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

// frontMatter is the wire shape of a prompt file's YAML block. Unknown keys are rejected so a
// misspelled roster key fails at load rather than being silently ignored.
type frontMatter struct {
	Description string      `yaml:"description"`
	Model       string      `yaml:"model"`
	Effort      string      `yaml:"effort"`
	Executor    string      `yaml:"executor"`
	Agents      []agentYAML `yaml:"agents"`
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
	if a.Name == "" {
		return errors.New("roster entry has no name")
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
	fm, d, err := parseFile(meta, body)
	if err != nil {
		return nil, err
	}

	p := &Profile{doc: d, Name: name, model: fm.Model, effort: fm.Effort}
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
	fm, d, err := parseFile(meta, body)
	if err != nil {
		return nil, err
	}

	st := &Stage{doc: d, Name: name, Executor: fm.Executor, Model: fm.Model, Effort: fm.Effort}
	if st.Executor == "" {
		st.Executor = executorClaude
	}
	return st, nil
}

func parseLens(meta, body []byte) (doc, error) {
	_, d, err := parseFile(meta, body)
	return d, err
}
