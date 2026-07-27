package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/jessevdk/go-flags"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/prompt"
)

// defaultConfig is the commented-out INI template --init materializes. It lives in package main rather
// than under app/prompt/defaults because it is settings, not prompt content.
//
//go:embed defaults/config
var defaultConfig []byte

// placeholderNone is what an absent optional context file expands to. A path that does not exist would
// leave an agent unable to tell absence from a broken run.
const placeholderNone = "none provided"

// the precedence layer that supplied a knob's final value, as reported by knobOrigins.
const (
	originFlag    = "flag"
	originProject = "project"
	originUser    = "user"
	originDefault = "default"
)

const (
	projectDirName = ".revmux"
	configFileName = "config"
	scopeFileName  = "scope.md"
	goalFileName   = "goal.md"
	profFileName   = "profile.md"
	contextDirName = "context"
	runTimeFormat  = "20060102T150405Z"
)

// options is the full CLI surface. Only the runtime knobs carry an ini-name; everything shaping a
// review lives in the prompt tree, and the meta flags make no sense in a config file.
type options struct {
	Task           string   `long:"task" no-ini:"true" description:"name of the task directory holding the review context"`
	Run            string   `long:"run" no-ini:"true" description:"name for this round of the review"`
	Lenses         []string `long:"lenses" no-ini:"true" description:"lens set replacing the profile roster"`
	WorkDir        string   `long:"workdir" no-ini:"true" description:"directory the review subprocesses run in"`
	MinConfidence  int      `long:"min-confidence" no-ini:"true" default:"0" description:"drop findings below this confidence"`
	NoSynthesis    bool     `long:"no-synthesis" no-ini:"true" description:"skip the synthesis stage"`
	NoVerify       bool     `long:"no-verify" no-ini:"true" description:"skip the verification stage"`
	NoTUI          bool     `long:"no-tui" no-ini:"true" description:"disable the terminal UI"`
	JSON           bool     `long:"json" no-ini:"true" description:"write the report as JSON"`
	PreserveAPIKey bool     `long:"preserve-anthropic-api-key" no-ini:"true" description:"pass ANTHROPIC_API_KEY to the model CLIs"`

	IdleTimeout  time.Duration `long:"idle-timeout" ini-name:"idle-timeout" default:"2m" description:"kill and retry an agent after this long with no output"`
	HardTimeout  time.Duration `long:"hard-timeout" ini-name:"hard-timeout" default:"20m" description:"kill an agent after this long, per attempt"`
	StaggerDelay time.Duration `long:"stagger-delay" ini-name:"stagger-delay" default:"30s" description:"how long to wait for the first agent before releasing the rest"`
	MaxParallel  int           `long:"max-parallel" ini-name:"max-parallel" default:"4" description:"how many agents run at once"`
	VerifyGroups int           `long:"verify-groups" ini-name:"verify-groups" default:"6" description:"cap on the number of verifier groups"`
	TasksDir     string        `long:"tasks-dir" ini-name:"tasks-dir" default:"./.revmux/tasks" description:"root directory holding task directories"`
	KeepRuns     int           `long:"keep-runs" ini-name:"keep-runs" default:"10" description:"how many runs to keep per task"`
	AutoExit     time.Duration `long:"auto-exit" ini-name:"auto-exit" default:"0s" description:"close the terminal UI this long after the report arrives"`
	Profile      string        `long:"profile" ini-name:"profile" default:"comprehensive" description:"profile naming the roster to run"`

	ConfigDir    string `long:"config-dir" no-ini:"true" description:"directory holding the config file and the prompt tree"`
	Init         bool   `long:"init" no-ini:"true" description:"write the commented-out config template to ./.revmux/"`
	DumpDefaults string `long:"dump-defaults" no-ini:"true" description:"extract the embedded prompt tree into a directory"`
	Version      bool   `long:"version" no-ini:"true" description:"show version and exit"`

	Config configCmd `command:"config" description:"print the resolved configuration as JSON"`

	layers      configLayers
	knobOrigins map[string]string
	showConfig  bool
}

// configLayers are the two on-disk roots searched ahead of the embedded defaults, resolved once during
// parsing so the INI load and the prompt tree can never disagree about where they are.
type configLayers struct {
	project string // ./.revmux, empty when it is absent or collapsed into the user layer
	user    string // --config-dir, or ~/.config/revmux
}

// reviewContext is one task directory, fully resolved. Every field is an absolute path and no context
// file is ever opened: variables carry paths and the agent reads them itself.
type reviewContext struct {
	TaskDir string
	Scope   string
	Goal    string
	Profile string
	Context string
	WorkDir string
}

// parseArgs parses the command line, then layers the project and user INI files underneath it, and
// records which layer supplied each knob.
func parseArgs(args []string) (options, error) {
	var o options
	p := flags.NewParser(&o, flags.HelpFlag|flags.PassDoubleDash)
	p.SubcommandsOptional = true // a plain `revmux --task pr-123` carries no command word
	o.Config.opts = &o           // the back-pointer is only how Execute records the selection during this parse
	if _, err := p.ParseArgs(args); err != nil {
		return o, fmt.Errorf("parse arguments: %w", err)
	}
	o.Lenses = splitList(o.Lenses)

	layers, err := resolveLayers(o.ConfigDir)
	if err != nil {
		return o, err
	}
	o.layers = layers

	origins := map[string]string{}
	for _, name := range knobNames() {
		// an explicitly passed flag is the one thing that reaches Set without setDefault, so it is
		// the only case IsSet distinguishes once ParseArgs has applied every struct default
		if opt := p.FindOptionByLongName(name); opt != nil && opt.IsSet() && !opt.IsSetDefault() {
			origins[name] = originFlag
		}
	}

	for _, l := range []struct{ name, dir string }{{originProject, layers.project}, {originUser, layers.user}} {
		if l.dir == "" {
			continue
		}
		if err := loadIni(p, filepath.Join(l.dir, configFileName), l.name, origins); err != nil {
			return o, err
		}
	}

	for _, name := range knobNames() {
		if _, ok := origins[name]; !ok {
			origins[name] = originDefault
		}
	}
	o.knobOrigins = origins
	return o, nil
}

// loadIni layers one config file under everything already set and attributes its keys to that layer.
// Both INI layers parse as defaults: without that, go-flags calls Set and every value in the file
// overwrites the command line, inverting the precedence silently.
func loadIni(p *flags.Parser, file, layer string, origins map[string]string) error {
	keys, err := iniKeys(file)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	ini := flags.NewIniParser(p)
	ini.ParseAsDefaults = true
	if err := ini.ParseFile(file); err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}

	// first writer wins, so a key already claimed by the command line or the project layer stays there
	for _, k := range keys {
		if _, claimed := origins[k]; !claimed {
			origins[k] = layer
		}
	}
	return nil
}

// iniKeys reports the keys an INI file actually sets, lowercased. It is the only sound way to tell the
// project layer from the user layer: both route through setDefault and leave the same flags behind.
// A missing file has no keys and is not an error.
func iniKeys(file string) ([]string, error) {
	data, err := os.ReadFile(file) //nolint:gosec // the path is the operator's own config location
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}

	var keys []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		if k, _, ok := strings.Cut(line, "="); ok {
			keys = append(keys, strings.ToLower(strings.TrimSpace(k)))
		}
	}
	return keys, nil
}

// knobNames lists the INI-backed settings, in struct order. The config key is the long flag name, so a
// reader of --help can guess it.
func knobNames() []string {
	t := reflect.TypeFor[options]()
	out := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		if f.Tag.Get("no-ini") != "" || f.Tag.Get("long") == "" {
			continue
		}
		out = append(out, f.Tag.Get("long"))
	}
	return out
}

// resolveLayers locates the two on-disk config roots. The project layer is auto-detected in the process
// working directory and simply absent when the directory is not there.
func resolveLayers(configDir string) (configLayers, error) {
	user := configDir
	if user == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return configLayers{}, fmt.Errorf("locate home directory: %w", err)
		}
		user = filepath.Join(home, ".config", "revmux")
	}
	user, err := filepath.Abs(user)
	if err != nil {
		return configLayers{}, fmt.Errorf("resolve config dir %q: %w", configDir, err)
	}

	project, err := filepath.Abs(projectDirName)
	if err != nil {
		return configLayers{}, fmt.Errorf("resolve project config dir: %w", err)
	}
	if fi, statErr := os.Stat(project); statErr != nil || !fi.IsDir() {
		project = ""
	}
	if project != "" && sameDir(project, user) {
		project = "" // one directory loaded as two layers would make the layer origins wrong
	}
	return configLayers{project: project, user: user}, nil
}

// sameDir compares two directories after evaluating symlinks, since a temp dir under /var is really
// /private/var on macOS and a lexical compare misses the collision in exactly the tests that catch it.
func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

// splitList flattens a repeated flag that also accepts a comma-separated value.
func splitList(in []string) []string {
	var out []string
	for _, v := range in {
		for part := range strings.SplitSeq(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// promptOpts hands the resolved layers to the prompt tree, so config and prompts are searched in the
// same two places.
func (o options) promptOpts() prompt.LoadOpts {
	return prompt.LoadOpts{ProjectDir: o.layers.project, UserDir: o.layers.user}
}

// executorOpts takes the resolved context rather than the raw flag so the directory an agent is told to
// review is the one its process actually runs in.
func (o options) executorOpts(rc reviewContext, clk executor.Clock) executor.Opts {
	return executor.Opts{
		IdleTimeout:    o.IdleTimeout,
		HardTimeout:    o.HardTimeout,
		WorkDir:        rc.WorkDir,
		PreserveAPIKey: o.PreserveAPIKey,
		Clock:          clk,
	}
}

// runName resolves --run to its UTC-timestamp default. The resolved value travels down; nothing
// downstream re-derives it.
func (o options) runName(clk executor.Clock) string {
	if o.Run != "" {
		return o.Run
	}
	return clk.Now().UTC().Format(runTimeFormat)
}

// promptSet loads the prompt tree and confirms the selected profile resolves.
func (o options) promptSet() (*prompt.Set, error) {
	set, err := prompt.Load(o.promptOpts())
	if err != nil {
		return nil, fmt.Errorf("load prompts: %w", err)
	}
	if _, err := set.Profile(o.Profile); err != nil {
		return nil, fmt.Errorf("resolve profile: %w", err)
	}
	return set, nil
}

// resolveContext stats the task directory and returns its absolute paths. Nothing here opens a file:
// scope emptiness is a size check, so a large scope can never reach a prompt.
func (o options) resolveContext() (reviewContext, error) {
	if err := o.checkNames(); err != nil {
		return reviewContext{}, err
	}

	dir, err := o.taskDir()
	if err != nil {
		return reviewContext{}, err
	}

	rc := reviewContext{TaskDir: dir}
	if rc.Scope, err = o.contextFile(filepath.Join(dir, scopeFileName)); err != nil {
		return reviewContext{}, err
	}
	if rc.Scope == "" {
		return reviewContext{}, fmt.Errorf("%s is required and must not be empty", filepath.Join(dir, scopeFileName))
	}
	if rc.Goal, err = o.contextFile(filepath.Join(dir, goalFileName)); err != nil {
		return reviewContext{}, err
	}
	if rc.Profile, err = o.contextFile(filepath.Join(dir, profFileName)); err != nil {
		return reviewContext{}, err
	}
	if rc.Context, err = o.contextDir(filepath.Join(dir, contextDirName)); err != nil {
		return reviewContext{}, err
	}
	if rc.WorkDir, err = o.workDir(); err != nil {
		return reviewContext{}, err
	}
	return rc, nil
}

// taskDir joins the task name onto the tasks root and verifies the result stays inside it. Containment
// is re-checked on the resolved path because a symlink inside the root defeats the lexical test.
func (o options) taskDir() (string, error) {
	root, err := filepath.Abs(o.TasksDir)
	if err != nil {
		return "", fmt.Errorf("resolve tasks dir %q: %w", o.TasksDir, err)
	}
	dir := filepath.Join(root, o.Task)

	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("task directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("task directory %s is not a directory", dir)
	}

	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve task directory %s: %w", dir, err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve tasks dir %s: %w", root, err)
	}
	if !strings.HasPrefix(realDir, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("task directory %s escapes the tasks root %s", realDir, realRoot)
	}
	return dir, nil
}

// contextFile returns the path of a context file, an empty string when it is absent or empty, and an
// error when it exists as something other than a file.
func (o options) contextFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory, want a file", path)
	}
	if fi.Size() == 0 {
		return "", nil
	}
	return path, nil
}

// contextDir returns the path of the context directory, or an empty string when it is absent or holds
// nothing — a path to an empty directory tells an agent less than the placeholder does.
func (o options) contextDir(path string) (string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is a file, want a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(entries) == 0 {
		return "", nil
	}
	return path, nil
}

// workDir resolves --workdir, defaulting to the process working directory.
func (o options) workDir() (string, error) {
	if o.WorkDir == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		return dir, nil
	}
	dir, err := filepath.Abs(o.WorkDir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %q: %w", o.WorkDir, err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workdir %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workdir %s is not a directory", dir)
	}
	return dir, nil
}

// checkNames rejects a task or run name before it is joined into any path. Both are caller-supplied and
// become filesystem paths, so a separator or a `..` would let revmux write over caller-authored context.
func (o options) checkNames() error {
	if err := o.checkName("--task", o.Task); err != nil {
		return err
	}
	if o.Run == "" {
		return nil
	}
	return o.checkName("--run", o.Run)
}

func (o options) checkName(flag, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%s is empty", flag)
	case filepath.IsAbs(name):
		return fmt.Errorf("%s %q is absolute", flag, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%s %q contains a path separator", flag, name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%s %q references a parent directory", flag, name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%s %q starts with a dot", flag, name)
	}
	return nil
}

// initConfig materializes the commented-out template under ./.revmux/, leaving a customized file alone.
func (o options) initConfig(w io.Writer) error {
	dir, err := filepath.Abs(projectDirName)
	if err != nil {
		return fmt.Errorf("resolve project config dir: %w", err)
	}
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return fmt.Errorf("create %s: %w", dir, mkErr)
	}

	path := filepath.Join(dir, configFileName)
	keys, err := iniKeys(path)
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		_, _ = fmt.Fprintf(w, "%s is customized, left unchanged\n", path)
		return nil
	}
	if err := os.WriteFile(path, defaultConfig, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(w, "wrote %s\n", path)
	return nil
}

// dumpDefaults extracts the embedded prompt tree, skipping every file already present so an edited
// override is never overwritten.
func (o options) dumpDefaults(w io.Writer) error {
	tree := prompt.Defaults()
	err := fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dst := filepath.Join(o.DumpDefaults, filepath.FromSlash(p))
		if _, statErr := os.Stat(dst); statErr == nil {
			_, _ = fmt.Fprintf(w, "%s already present, left unchanged\n", dst)
			return nil
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		_, _ = fmt.Fprintf(w, "wrote %s\n", dst)
		return nil
	})
	if err != nil {
		return fmt.Errorf("dump defaults to %s: %w", o.DumpDefaults, err)
	}
	return nil
}

// vars assembles the substitution map. Every value is an absolute path, and anything absent carries the
// placeholder rather than a path that does not exist.
func (rc reviewContext) vars() prompt.Vars {
	v := prompt.Vars{
		"SCOPE":   rc.Scope,
		"GOAL":    rc.Goal,
		"PROFILE": rc.Profile,
		"CONTEXT": rc.Context,
		"WORKDIR": rc.WorkDir,
	}
	for name, val := range v {
		if val == "" {
			v[name] = placeholderNone
		}
	}
	return v
}
