// Package prompt loads the review prompt tree — profiles, lenses and stage prompts — resolving every
// file across the project, user and embedded layers, and composes the prompt one agent or stage
// actually receives.
//
// Nothing here reads a repository or opens a caller's context file: context variables expand to
// absolute paths and the agent reads them itself.
package prompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Precedence layers reported by Provenance, highest first.
const (
	LayerProject  = "project"
	LayerUser     = "user"
	LayerEmbedded = "embedded"
)

// variables is the closed substitution vocabulary. A prompt file naming anything else fails at load,
// which is what makes a typo loud instead of silent.
var variables = []string{"SCOPE", "GOAL", "PROFILE", "CONTEXT", "WORKDIR", "FINDINGS", "SOURCES"}

var varPattern = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// treeGlobs are the three shapes the prompt tree holds. The profiles glob runs first, and the
// prompts glob does not cross a separator, so a profile is never also loaded as a stage.
var treeGlobs = []string{"prompts/profiles/*.md", "prompts/*.md", "lenses/*.md"}

// LoadOpts names the two on-disk layers searched ahead of the embedded defaults. An empty directory
// drops that layer rather than failing.
type LoadOpts struct {
	ProjectDir string
	UserDir    string
}

// FileOrigin records which layer supplied one loaded file and what it contained. Two runs of one
// task can use different lens text, so app/archive writes this to manifest.json.
type FileOrigin struct {
	Path   string `json:"path"`
	Layer  string `json:"layer"`
	Source string `json:"source,omitempty"`
	Hash   string `json:"hash"`
}

// LensInfo is one lens as reported by `revmux config`, so a caller composing --lenses can tell what
// a lens covers without reading its body.
type LensInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// doc is the front matter and body every prompt file shares.
type doc struct {
	Description string
	Body        string
}

// Set is the resolved prompt tree: what each file resolved to, not what is embedded.
type Set struct {
	profiles map[string]*Profile
	stages   map[string]*Stage
	lenses   map[string]doc
	origins  []FileOrigin
}

type fileRef struct {
	layer  string
	source string
	data   []byte
}

// layer is one searchable root in the precedence chain. root is empty for the embedded layer, which
// is what makes a FileOrigin report no on-disk source for it.
type layer struct {
	name string
	root string
	fsys fs.FS
}

// Load resolves the prompt tree per file across project, user and embedded layers and parses every
// file it finds. Overriding one lens does not orphan the others, and deleting one on disk falls back
// to the embedded copy rather than disabling it.
func Load(opts LoadOpts) (*Set, error) {
	files, err := collectFiles(opts)
	if err != nil {
		return nil, err
	}

	set := &Set{profiles: map[string]*Profile{}, stages: map[string]*Stage{}, lenses: map[string]doc{}}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		ref := files[rel]
		meta, body, err := splitFrontMatter(ref.data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		if err := set.add(rel, meta, body); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		set.origins = append(set.origins, FileOrigin{Path: rel, Layer: ref.layer, Source: ref.source, Hash: hashOf(ref.data)})
	}

	if err := set.validate(); err != nil {
		return nil, err
	}
	return set, nil
}

// Profile returns the named profile.
func (s *Set) Profile(name string) (*Profile, error) {
	p, ok := s.profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q, have %s", name, strings.Join(s.ProfileNames(), ", "))
	}
	return p, nil
}

// Stage returns the named stage prompt, `synthesis` or `verify`.
func (s *Set) Stage(name string) (*Stage, error) {
	st, ok := s.stages[name]
	if !ok {
		return nil, fmt.Errorf("unknown stage %q", name)
	}
	return st, nil
}

// Lenses reports every lens that resolved, with the description of the file that won. A description
// is never inherited from the embedded default: an override is different text.
func (s *Set) Lenses() []LensInfo {
	out := make([]LensInfo, 0, len(s.lenses))
	for _, name := range slices.Sorted(maps.Keys(s.lenses)) {
		out = append(out, LensInfo{Name: name, Description: s.lenses[name].Description})
	}
	return out
}

// LensNames is the validation input for a roster or a --lenses override, so the composition root
// does not assemble one by hand out of Lenses.
func (s *Set) LensNames() map[string]struct{} {
	out := make(map[string]struct{}, len(s.lenses))
	for name := range s.lenses {
		out[name] = struct{}{}
	}
	return out
}

// ProfileNames lists every profile that resolved, sorted.
func (s *Set) ProfileNames() []string { return slices.Sorted(maps.Keys(s.profiles)) }

// Provenance reports the winning layer and content hash per loaded file, sorted by path.
func (s *Set) Provenance() []FileOrigin { return slices.Clone(s.origins) }

func (s *Set) lens(name string) (string, error) {
	d, ok := s.lenses[name]
	if !ok {
		return "", fmt.Errorf("unknown lens %q", name)
	}
	return d.Body, nil
}

func (s *Set) add(rel string, meta, body []byte) error {
	name := strings.TrimSuffix(path.Base(rel), ".md")
	switch {
	case strings.HasPrefix(rel, "prompts/profiles/"):
		p, err := parseProfile(name, meta, body)
		if err != nil {
			return err
		}
		s.profiles[name] = p
	case strings.HasPrefix(rel, "prompts/"):
		st, err := parseStage(name, meta, body)
		if err != nil {
			return err
		}
		s.stages[name] = st
	default:
		d, err := parseLens(meta, body)
		if err != nil {
			return err
		}
		s.lenses[name] = d
	}
	return nil
}

func (s *Set) validate() error {
	known := s.LensNames()
	for _, name := range s.ProfileNames() {
		if err := s.profiles[name].validate(known); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.stages)) {
		if err := s.stages[name].validate(); err != nil {
			return err
		}
	}
	return nil
}

func collectFiles(opts LoadOpts) (map[string]fileRef, error) {
	embedded, err := fs.Sub(defaults, defaultsRoot)
	if err != nil {
		return nil, fmt.Errorf("open embedded defaults: %w", err)
	}

	// lowest precedence first, so a higher layer overwrites what a lower one already claimed
	layers := []layer{{name: LayerEmbedded, fsys: embedded}}
	for _, l := range []struct{ name, root string }{{LayerUser, opts.UserDir}, {LayerProject, opts.ProjectDir}} {
		if l.root == "" {
			continue
		}
		layers = append(layers, layer{name: l.name, root: l.root, fsys: os.DirFS(l.root)})
	}

	files := map[string]fileRef{}
	for _, l := range layers {
		if err := l.collect(files); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (l layer) collect(files map[string]fileRef) error {
	for _, g := range treeGlobs {
		matches, err := fs.Glob(l.fsys, g)
		if err != nil {
			return fmt.Errorf("glob %s in %s layer: %w", g, l.name, err)
		}
		for _, m := range matches {
			data, err := fs.ReadFile(l.fsys, m)
			if err != nil {
				return fmt.Errorf("read %s from %s layer: %w", m, l.name, err)
			}
			src := ""
			if l.root != "" {
				src = filepath.Join(l.root, filepath.FromSlash(m))
			}
			files[m] = fileRef{layer: l.name, source: src, data: data}
		}
	}
	return nil
}

// splitFrontMatter separates a leading `---` delimited YAML block from the body. A file with no
// front matter is all body, which is what makes description optional on an override.
func splitFrontMatter(b []byte) (meta, body []byte, err error) {
	const marker = "---"
	if !bytes.HasPrefix(b, []byte(marker+"\n")) {
		return nil, b, nil
	}

	rest := b[len(marker)+1:]
	for off := 0; off < len(rest); {
		i := bytes.Index(rest[off:], []byte("\n"+marker))
		if i < 0 {
			break
		}
		at := off + i
		after := at + 1 + len(marker)
		if after == len(rest) || rest[after] == '\n' {
			return rest[:at+1], bytes.TrimPrefix(rest[after:], []byte("\n")), nil
		}
		off = at + 1
	}
	return nil, nil, errors.New("front matter is opened but never closed")
}

func parseFile(meta, body []byte) (frontMatter, doc, error) {
	var fm frontMatter
	if len(bytes.TrimSpace(meta)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(meta))
		dec.KnownFields(true)
		if err := dec.Decode(&fm); err != nil && !errors.Is(err, io.EOF) {
			return fm, doc{}, fmt.Errorf("parse front matter: %w", err)
		}
	}
	if err := checkVariables(body); err != nil {
		return fm, doc{}, err
	}
	return fm, doc{Description: fm.Description, Body: strings.TrimSpace(string(body))}, nil
}

func checkVariables(body []byte) error {
	for _, m := range varPattern.FindAllSubmatch(body, -1) {
		if !slices.Contains(variables, string(m[1])) {
			return fmt.Errorf("unknown variable {{%s}}, vocabulary is %s", m[1], strings.Join(variables, ", "))
		}
	}
	return nil
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
