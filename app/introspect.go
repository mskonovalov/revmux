package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
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
//
// TasksError and WorkDirError carry why a field is the raw flag rather than a resolved path. An
// unreadable tasks root reported as no tasks at all is the same wrong advice an unreported meta_error
// gives one level down: it reads as "nothing is there" and mints a duplicate id.
type pathInfo struct {
	TasksDir     string     `json:"tasks_dir"`
	TasksError   string     `json:"tasks_error,omitempty"`
	ConfigDir    string     `json:"config_dir"`
	ProjectDir   string     `json:"project_dir,omitempty"`
	WorkDir      string     `json:"workdir"`
	WorkDirError string     `json:"workdir_error,omitempty"`
	Tasks        []taskInfo `json:"tasks"`
}

// taskInfo is one existing task: its id, what its task.md says about it, and the rounds already
// recorded under it. This is what a caller model matches an in-flight review against, so an id alone
// is not enough — without the anchors it cannot tell pr-123 from a near-duplicate it is about to mint.
//
// MetaError carries why the anchors are empty when task.md would not parse, and RoundsError why the
// rounds are empty when the task directory could not be read. The task is still listed either way — one
// omitted here is one a caller gives a second id to — but an empty field on its own reads as "there is
// nothing here", which is the opposite advice.
type taskInfo struct {
	ID string `json:"id"`
	task.Meta
	MetaError   string   `json:"meta_error,omitempty"`
	Rounds      []string `json:"rounds"`
	RoundsError string   `json:"rounds_error,omitempty"`
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

// paths resolves the two roots and the working directory, and lists the tasks already under the tasks
// root. Each part reports its own failure instead of a plausible-looking value: a workdir that would not
// resolve is a review that dies later, and an unreadable tasks root is not an empty one. An absent tasks
// root is a clean install and neither.
func (o options) paths() pathInfo {
	p := pathInfo{TasksDir: o.TasksDir, ConfigDir: o.layers.user, ProjectDir: o.layers.project,
		WorkDir: o.WorkDir, Tasks: []taskInfo{}}
	if dir, err := o.workDir(); err != nil {
		p.WorkDirError = err.Error()
	} else {
		p.WorkDir = dir
	}

	abs, err := filepath.Abs(o.TasksDir)
	if err != nil {
		p.TasksError = fmt.Sprintf("resolve tasks dir %q: %s", o.TasksDir, err)
		return p
	}
	p.TasksDir = abs

	tasks, err := o.tasks(abs)
	if err != nil {
		p.TasksError = err.Error()
	}
	p.Tasks = tasks
	return p
}

// tasks reports every task under the tasks root with the metadata a caller matches on. A task.md that
// is absent or will not parse leaves the anchors empty rather than hiding the task: the directory is
// there either way, and a task omitted from this list is one a caller mints a second id for.
//
// A parse failure is reported on the entry rather than dropped. Silently empty anchors are how a typo'd
// key becomes a duplicate task id — the exact failure task.md exists to prevent. Rounds come from
// task.Rounds, the same enumeration the prior-round inventory is built from.
//
// Which ids exist is task.List, the single enumerator `revmux stats` reads too, so the catalog and the
// aggregate cannot disagree about what a task is.
func (o options) tasks(root string) ([]taskInfo, error) {
	out := []taskInfo{}
	names, err := task.List(root)
	if err != nil {
		return out, fmt.Errorf("list tasks: %w", err)
	}

	for _, name := range names {
		dir := filepath.Join(root, name)
		meta, loadErr := task.Load(dir)
		info := taskInfo{ID: name, Meta: meta, Rounds: []string{}}
		if loadErr != nil {
			info.MetaError = loadErr.Error()
		}
		rounds, roundsErr := task.Rounds(dir)
		if roundsErr != nil {
			info.RoundsError = roundsErr.Error()
		} else {
			info.Rounds = rounds
		}
		out = append(out, info)
	}
	return out, nil
}
