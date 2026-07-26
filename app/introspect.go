package main

import (
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/umputun/revmux/app/prompt"
)

// catalogStages are the stage prompts the catalog reports, in pipeline order.
var catalogStages = []string{"synthesis", "verify"}

// configCmd is the `revmux config` subcommand. It only records the selection: go-flags calls Execute
// from inside parseArgs, before the injected writers and the loaded prompt tree exist, so the catalog
// is built and written by run.
type configCmd struct {
	opts *options
}

// catalog is what `revmux config` emits — the resolved configuration a caller model needs to compose
// an invocation without reading the prompt tree.
type catalog struct {
	Knobs      []knob            `json:"knobs"`
	Profiles   []profileInfo     `json:"profiles"`
	Lenses     []prompt.LensInfo `json:"lenses"`
	Stages     []stageInfo       `json:"stages"`
	Vocabulary vocabulary        `json:"vocabulary"`
	Paths      pathInfo          `json:"paths"`
}

// knob is one runtime setting with the precedence layer that supplied it. The value alone would not
// tell a caller whether it is worth passing the flag.
type knob struct {
	Name   string `json:"name"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// profileInfo is a profile and the roster it would dispatch, colors included.
type profileInfo struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Roster      []prompt.AgentSpec `json:"roster"`
}

// stageInfo is one stage prompt. It names its own binary, model and effort, so a caller reasoning
// about which model judges the findings needs them reported.
type stageInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Executor    string `json:"executor"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
}

// vocabulary is the accepted front-matter values, read from the same constants validate checks
// against so a new effort level cannot ship working but undiscoverable.
type vocabulary struct {
	Executors []string `json:"executors"`
	Efforts   []string `json:"efforts"`
}

// pathInfo is where this invocation resolved its directories, plus the tasks that already exist —
// a --run name collides with an existing round and a caller cannot avoid that blind.
type pathInfo struct {
	TasksDir   string   `json:"tasks_dir"`
	ConfigDir  string   `json:"config_dir"`
	ProjectDir string   `json:"project_dir,omitempty"`
	WorkDir    string   `json:"workdir"`
	Tasks      []string `json:"tasks"`
}

// Execute records that the config command was selected. Writing the catalog here would bypass the
// injected stdout and leave nothing for a test to capture.
func (c *configCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showConfig = true
	return nil
}

// catalog assembles what resolved, never what is embedded: a user who overrode one lens and added
// another must see his own tree, or this describes a review that will not happen.
func (o options) catalog(set *prompt.Set) catalog {
	c := catalog{
		Knobs:      o.knobs(),
		Lenses:     set.Lenses(),
		Vocabulary: vocabulary{Executors: prompt.Executors(), Efforts: prompt.Efforts()},
		Paths:      o.paths(),
	}

	known := set.LensNames()
	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		if err != nil {
			continue
		}
		// the roster resolves for every loaded profile: Load rejected any that did not validate
		roster, err := p.Roster(nil, known)
		if err != nil {
			continue
		}
		c.Profiles = append(c.Profiles, profileInfo{Name: name, Description: p.Description, Roster: roster})
	}

	for _, name := range catalogStages {
		st, err := set.Stage(name)
		if err != nil {
			continue
		}
		c.Stages = append(c.Stages, stageInfo{Name: name, Description: st.Description,
			Executor: st.Executor, Model: st.Model, Effort: st.Effort})
	}
	return c
}

// knobs reports every INI-backed setting with the layer parseArgs recorded during the load. The
// origins are read, never re-derived: a second tracking mechanism would be one more thing to drift.
func (o options) knobs() []knob {
	v := reflect.ValueOf(o)
	out := make([]knob, 0, v.NumField())
	for f := range v.Type().Fields() {
		if f.Tag.Get("no-ini") != "" || f.Tag.Get("long") == "" {
			continue
		}
		name := f.Tag.Get("long")
		val := v.FieldByIndex(f.Index).Interface()
		if d, ok := val.(time.Duration); ok {
			val = d.String()
		}
		out = append(out, knob{Name: name, Value: val, Source: o.knobOrigins[name]})
	}
	return out
}

// paths resolves the two roots and the working directory, and lists the tasks already under the
// tasks root. An absent tasks root is a clean install, not an error.
func (o options) paths() pathInfo {
	p := pathInfo{TasksDir: o.TasksDir, ConfigDir: o.layers.user, ProjectDir: o.layers.project, WorkDir: o.WorkDir}
	if abs, err := filepath.Abs(o.TasksDir); err == nil {
		p.TasksDir = abs
	}
	if dir, err := o.workDir(); err == nil {
		p.WorkDir = dir
	}

	p.Tasks = []string{}
	entries, err := os.ReadDir(p.TasksDir)
	if err != nil {
		return p
	}
	for _, e := range entries {
		if e.IsDir() {
			p.Tasks = append(p.Tasks, e.Name())
		}
	}
	return p
}
