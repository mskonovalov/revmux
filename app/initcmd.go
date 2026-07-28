package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/umputun/revmux/app/prompt"
)

// initCmd is the `revmux init` subcommand. It only records the selection: go-flags calls Execute from
// inside parseArgs, before the injected writers and the loaded prompt tree exist, so the tree is
// materialized and reported by run.
type initCmd struct {
	opts *options
}

// initPaths is what `revmux init` emits — the local tree a user edits, reported rather than described,
// so no caller composes a path into it.
type initPaths struct {
	Dir    string     `json:"dir"`
	Config string     `json:"config"`
	Files  []initFile `json:"files"`
}

// initFile is one prompt file under ./.revmux/: where it is, which layer supplied its content, and
// whether this call wrote it. One already there is reported and left exactly as it is.
type initFile struct {
	Path    string `json:"path"`
	Layer   string `json:"layer"`
	Created bool   `json:"created"`
}

// Execute records that the init command was selected. Materializing here would write through the real
// os.Stdout and leave nothing for a test to capture.
func (c *initCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showInit = true
	return nil
}

// writeInitPaths materializes ./.revmux/ and prints what is in it as JSON. Like the catalog and the
// scaffolded round it is a carve-out in "stdout belongs to the report": no pipeline, archive or TUI
// exists yet, so there is nothing for it to collide with.
//
// The prompt tree is loaded directly rather than through promptSet: a caller initializing a project has
// not chosen a --profile yet, and refusing to materialize until he has is backwards.
func (o runOpts) writeInitPaths() error {
	set, err := prompt.Load(o.opts.promptOpts())
	if err != nil {
		return fmt.Errorf("load prompts: %w", err)
	}
	dir, err := o.opts.projectDir()
	if err != nil {
		return err
	}
	// settings ship commented out so an upgrade can still overwrite them, which is the whole reason the
	// config is not materialized the way the prompt files are; its prose report is not the payload
	if cfgErr := o.opts.initConfig(io.Discard); cfgErr != nil {
		return cfgErr
	}
	files, err := o.materializePrompts(set)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(o.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(initPaths{Dir: dir, Config: filepath.Join(dir, configFileName), Files: files}); err != nil {
		return fmt.Errorf("write init paths: %w", err)
	}
	return nil
}

// materializePrompts writes what each prompt file resolved to into ./.revmux/, leaving one already there
// untouched. The bytes are the winning file's own, front matter included: a stripped write produces a
// tree the next prompt.Load rejects, so init would break the project it just initialized.
func (o runOpts) materializePrompts(set *prompt.Set) ([]initFile, error) {
	dir, err := o.opts.projectDir()
	if err != nil {
		return nil, err
	}

	origins := set.Provenance()
	out := make([]initFile, 0, len(origins))
	for _, org := range origins {
		dst := filepath.Join(dir, filepath.FromSlash(org.Path))
		if _, statErr := os.Stat(dst); statErr == nil {
			out = append(out, initFile{Path: dst, Layer: org.Layer})
			continue
		}
		data, err := set.Content(org.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", org.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", dst, err)
		}
		out = append(out, initFile{Path: dst, Layer: org.Layer, Created: true})
	}
	return out, nil
}
