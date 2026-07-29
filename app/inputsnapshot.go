package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/umputun/revmux/app/task"
	"github.com/umputun/revmux/app/ui"
)

const (
	inputFileLimit  int64 = 1 << 20
	inputTotalLimit int64 = 8 << 20
	inputCountLimit       = 128
)

type inputLimits struct {
	fileBytes  int64
	totalBytes int64
	contexts   int
}

type inputSnapshotter struct {
	limits inputLimits
	used   int64
}

type inputFile struct {
	label    string
	relative string
	path     string
}

func (s *inputSnapshotter) load(rc reviewContext) []ui.InputDocument {
	docs := make([]ui.InputDocument, 0, 3)
	docs = append(docs,
		s.file(inputFile{label: "scope", relative: filepath.Join(task.InputDir, task.ScopeFile), path: rc.Scope}),
		s.optional(inputFile{label: "goal", relative: filepath.Join(task.InputDir, task.GoalFile), path: rc.Goal}),
		s.optional(inputFile{label: "profile", relative: filepath.Join(task.InputDir, task.ProfileFile), path: rc.Profile}),
	)
	return append(docs, s.context(rc.Context)...)
}

func (s *inputSnapshotter) optional(file inputFile) ui.InputDocument {
	if file.path == "" {
		return ui.InputDocument{
			Label: file.label, Path: filepath.ToSlash(file.relative), Markdown: true, Notice: "not provided",
		}
	}
	return s.file(file)
}

func (s *inputSnapshotter) context(dir string) []ui.InputDocument {
	if dir == "" {
		return []ui.InputDocument{{
			Label: "context", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "not provided",
		}}
	}

	rootInfo, err := os.Lstat(dir)
	if err != nil {
		return []ui.InputDocument{{
			Label: "context", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "cannot read context directory: " + err.Error(),
		}}
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return []ui.InputDocument{{
			Label: "context/", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "symlinked directory not traversed",
		}}
	}

	docs, limited := []ui.InputDocument{}, false
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if path == dir {
			if walkErr != nil {
				docs = append(docs, ui.InputDocument{
					Label: "context", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
					Notice: "cannot read context directory: " + walkErr.Error(),
				})
			}
			return nil
		}
		if len(docs) >= s.limits.contexts {
			limited = true
			return fs.SkipAll
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return fmt.Errorf("make context path relative: %w", relErr)
		}
		rel = filepath.ToSlash(rel)
		if walkErr == nil && entry.IsDir() {
			return nil
		}

		docPath := filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir, rel))
		if walkErr != nil {
			docs = append(docs, ui.InputDocument{
				Label: rel, Path: docPath, Notice: "cannot read: " + walkErr.Error(),
			})
			return nil //nolint:nilerr // the failure is preserved in this input's visible notice
		}

		info, statErr := os.Stat(path) // follows a file symlink, matching what an agent opening the path sees
		if statErr != nil {
			docs = append(docs, ui.InputDocument{
				Label: rel, Path: docPath, Notice: "cannot read: " + statErr.Error(),
			})
			return nil //nolint:nilerr // the failure is preserved in this input's visible notice
		}
		if info.IsDir() {
			docs = append(docs, ui.InputDocument{
				Label: rel + "/", Path: docPath, Notice: "symlinked directory not traversed",
			})
			return nil
		}
		if !info.Mode().IsRegular() {
			docs = append(docs, ui.InputDocument{
				Label: rel, Path: docPath, Notice: "not a regular file",
			})
			return nil
		}
		docs = append(docs, s.file(inputFile{label: rel, relative: docPath, path: path}))
		return nil
	})
	if err != nil {
		docs = append(docs, ui.InputDocument{
			Label: "context", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "cannot read context directory: " + err.Error(),
		})
	}
	if len(docs) == 0 && !limited {
		docs = append(docs, ui.InputDocument{
			Label: "context", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: "not provided",
		})
	}
	if limited {
		docs = append(docs, ui.InputDocument{
			Label: "more", Path: filepath.ToSlash(filepath.Join(task.InputDir, task.ContextDir)),
			Notice: fmt.Sprintf("more context entries omitted after the %d-file display limit", s.limits.contexts),
		})
	}
	return docs
}

func (s *inputSnapshotter) file(file inputFile) ui.InputDocument {
	doc := ui.InputDocument{
		Label: file.label, Path: filepath.ToSlash(file.relative),
		Markdown: strings.EqualFold(filepath.Ext(file.path), ".md"),
	}
	info, err := os.Stat(file.path)
	if err != nil {
		doc.Notice = "cannot read: " + err.Error()
		return doc
	}
	if !info.Mode().IsRegular() {
		doc.Notice = "not a regular file"
		return doc
	}

	remaining := s.limits.totalBytes - s.used
	allowed := min(s.limits.fileBytes, remaining)
	if allowed <= 0 {
		doc.Notice = fmt.Sprintf("not captured: the %d-byte snapshot display limit was reached", s.limits.totalBytes)
		return doc
	}

	f, err := os.Open(file.path)
	if err != nil {
		doc.Notice = "cannot read: " + err.Error()
		return doc
	}
	data, readErr := io.ReadAll(io.LimitReader(f, allowed+utf8.UTFMax))
	closeErr := f.Close()

	truncated := int64(len(data)) > allowed || info.Size() > allowed
	if int64(len(data)) > allowed {
		cut := int(allowed)
		if cut > 0 && data[cut-1] == '\r' && data[cut] == '\n' {
			cut--
		}
		data = data[:cut]
	}
	if truncated {
		data = s.trimIncompleteUTF8(data)
	}
	captured := len(data)
	s.used += int64(captured)

	data, ok := s.displayText(data)
	if !ok {
		doc.Notice = fmt.Sprintf("binary or unsafe text (%d bytes); contents not shown", info.Size())
		return doc
	}
	doc.Content = string(data)
	if truncated {
		doc.Notice = fmt.Sprintf("truncated: showing %d of %d bytes", captured, info.Size())
	}
	if readErr != nil {
		if doc.Notice != "" {
			doc.Notice += "; "
		}
		doc.Notice += "read stopped: " + readErr.Error()
	}
	if closeErr != nil {
		if doc.Notice != "" {
			doc.Notice += "; "
		}
		doc.Notice += "close failed: " + closeErr.Error()
	}
	return doc
}

func (*inputSnapshotter) trimIncompleteUTF8(data []byte) []byte {
	for range utf8.UTFMax {
		if utf8.Valid(data) {
			return data
		}
		if len(data) == 0 {
			return data
		}
		data = data[:len(data)-1]
	}
	return data
}

func (*inputSnapshotter) displayText(data []byte) ([]byte, bool) {
	if !utf8.Valid(data) {
		return nil, false
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return nil, false
		}
	}
	return []byte(text), true
}
