// Package archive writes the artifacts of one review run under the task directory's runs/<run>/.
//
// Everything above runs/ was written by the caller and is never modified or pruned. The artifacts
// exist so a review can be audited without re-running it: the composed prompt each process actually
// received, the findings after each stage, revmux's own decisions, and every agent's verbatim stream.
package archive

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// runsDir is the one directory revmux owns inside a task directory.
const runsDir = "runs"

// Opts is what an archive needs. The two names travel as fields rather than positionally: New(taskDir,
// run) would put two same-typed strings side by side, and swapping them compiles clean while writing
// the run somewhere else entirely.
type Opts struct {
	TaskDir string
	Run     string
	Keep    int
}

// Archive is one run's artifact directory.
type Archive struct {
	dir  string // this run's directory, symlinks resolved
	runs string // the runs/ root, symlinks resolved
	keep int
}

// runEntry is one prior run directory as Prune sees it.
type runEntry struct {
	path string
	at   time.Time
}

// New creates <TaskDir>/runs/<Run> and fails when it already exists rather than overwriting it: a
// round that went badly is exactly the one a later reflection agent wants to read.
func New(opts Opts) (*Archive, error) {
	if opts.TaskDir == "" {
		return nil, errors.New("task directory is empty")
	}
	if err := checkComponent("run name", opts.Run); err != nil {
		return nil, err
	}

	runs, err := filepath.Abs(filepath.Join(opts.TaskDir, runsDir))
	if err != nil {
		return nil, fmt.Errorf("resolve runs directory in %s: %w", opts.TaskDir, err)
	}
	if mkErr := os.MkdirAll(runs, 0o750); mkErr != nil {
		return nil, fmt.Errorf("create %s: %w", runs, mkErr)
	}
	// resolved here so every later containment check compares real paths: a temp dir under /var is
	// really /private/var on macOS, and a lexical compare misses the collision
	runs, err = filepath.EvalSymlinks(runs)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", runs, err)
	}

	dir := filepath.Join(runs, opts.Run)
	if err := os.Mkdir(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create run directory %s: %w", dir, err)
	}
	return &Archive{dir: dir, runs: runs, keep: opts.Keep}, nil
}

// Writer opens one artifact for writing, creating parent directories as needed. name is a path
// relative to the run directory, so one method serves the per-agent tees, the composed prompts, the
// per-stage findings, the manifest and the rendered report without the archive knowing what any of
// them mean.
//
// A separator is legal — prompts/agents/, stages/ and agents/ all need one — and only a path
// resolving outside the run directory is rejected, re-checked after evaluating symlinks because a
// symlink inside the run defeats the lexical test.
func (a *Archive) Writer(name string) (io.WriteCloser, error) {
	p, err := a.resolve(name)
	if err != nil {
		return nil, err
	}
	if mkErr := os.MkdirAll(filepath.Dir(p), 0o750); mkErr != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(p), mkErr)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // resolve confirmed the path stays inside the run directory
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	return f, nil
}

// Prune drops the oldest runs beyond Keep. It reads only runs/, so scope.md, goal.md, profile.md and
// context/ are never candidates however aggressive Keep is, and the run being written is never one.
func (a *Archive) Prune() error {
	entries, err := os.ReadDir(a.runs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", a.runs, err)
	}

	old := make([]runEntry, 0, len(entries))
	for _, e := range entries {
		p := filepath.Join(a.runs, e.Name())
		if !e.IsDir() || p == a.dir {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %s: %w", p, infoErr)
		}
		old = append(old, runEntry{path: p, at: info.ModTime()})
	}

	// the run being written counts toward keep, so one slot is already taken
	allowed := max(a.keep-1, 0)
	if len(old) <= allowed {
		return nil
	}

	slices.SortFunc(old, func(x, y runEntry) int { return x.at.Compare(y.at) })
	for _, r := range old[:len(old)-allowed] {
		if err := os.RemoveAll(r.path); err != nil {
			return fmt.Errorf("remove %s: %w", r.path, err)
		}
	}
	return nil
}

// resolve turns a run-relative artifact path into an absolute one inside the run directory.
func (a *Archive) resolve(name string) (string, error) {
	if name == "" {
		return "", errors.New("artifact name is empty")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("artifact %q is absolute", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact %q escapes the run directory", name)
	}

	p := filepath.Join(a.dir, clean)
	resolved, err := a.evalDeepest(p)
	if err != nil {
		return "", err
	}
	if !a.within(resolved) {
		return "", fmt.Errorf("artifact %q resolves to %s, outside %s", name, resolved, a.dir)
	}
	return p, nil
}

// evalDeepest resolves symlinks on the deepest existing ancestor of p and rejoins the part that does
// not exist yet, since an artifact is normally written into a directory tree Writer is about to create.
func (a *Archive) evalDeepest(p string) (string, error) {
	rest := ""
	for cur := p; ; {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve %s: %w", p, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("resolve %s: no existing ancestor", p)
		}
		rest, cur = filepath.Join(filepath.Base(cur), rest), parent
	}
}

func (a *Archive) within(p string) bool {
	return p == a.dir || strings.HasPrefix(p, a.dir+string(filepath.Separator))
}

// checkComponent rejects a caller-supplied name before it becomes one path component. It is the same
// rule package main applies to --task and --run, repeated here because Archive is reachable on its
// own and a run named `..` would write over the caller's context.
func checkComponent(what, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%s is empty", what)
	case filepath.IsAbs(name):
		return fmt.Errorf("%s %q is absolute", what, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%s %q contains a path separator", what, name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%s %q references a parent directory", what, name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%s %q starts with a dot", what, name)
	}
	return nil
}
