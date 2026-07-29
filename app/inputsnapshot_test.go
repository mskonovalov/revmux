package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputSnapshotter_load(t *testing.T) {
	root := t.TempDir()
	contextDir := filepath.Join(root, "context")
	require.NoError(t, os.MkdirAll(filepath.Join(contextDir, "nested"), 0o750))
	scope, goal := filepath.Join(root, "scope.md"), filepath.Join(root, "goal.md")
	require.NoError(t, os.WriteFile(scope, []byte("# Scope\nreview it"), 0o600))
	require.NoError(t, os.WriteFile(goal, []byte("find regressions"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "a.md"), []byte("**design**"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "nested", "b.json"), []byte(`{"ok":true}`), 0o600))

	s := &inputSnapshotter{limits: inputLimits{fileBytes: 1024, totalBytes: 4096, contexts: 10}}
	docs := s.load(reviewContext{Scope: scope, Goal: goal, Context: contextDir})

	require.Len(t, docs, 5)
	assert.Equal(t, []string{"scope", "goal", "profile", "a.md", "nested/b.json"},
		[]string{docs[0].Label, docs[1].Label, docs[2].Label, docs[3].Label, docs[4].Label})
	assert.Equal(t, "input/scope.md", docs[0].Path)
	assert.Equal(t, "# Scope\nreview it", docs[0].Content)
	assert.True(t, docs[0].Markdown)
	assert.Equal(t, "not provided", docs[2].Notice)
	assert.True(t, docs[3].Markdown)
	assert.False(t, docs[4].Markdown)
}

func TestInputSnapshotter_limits(t *testing.T) {
	t.Run("per-file and context count", func(t *testing.T) {
		root := t.TempDir()
		contextDir := filepath.Join(root, "context")
		require.NoError(t, os.Mkdir(contextDir, 0o750))
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("123456789"), 0o600))
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(contextDir, name), []byte(name), 0o600))
		}

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 5, totalBytes: 100, contexts: 2}}
		docs := s.load(reviewContext{Scope: scope, Context: contextDir})

		assert.Equal(t, "12345", docs[0].Content)
		assert.Contains(t, docs[0].Notice, "showing 5 of 9 bytes")
		require.Len(t, docs, 6, "three fixed tabs, two context files and one overflow tab")
		assert.Equal(t, "more", docs[5].Label)
		assert.Equal(t, "more context entries omitted after the 2-file display limit", docs[5].Notice)
	})

	t.Run("aggregate limit leaves the tab visible", func(t *testing.T) {
		root := t.TempDir()
		scope, goal := filepath.Join(root, "scope.md"), filepath.Join(root, "goal.md")
		require.NoError(t, os.WriteFile(scope, []byte("1234"), 0o600))
		require.NoError(t, os.WriteFile(goal, []byte("5678"), 0o600))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 10, totalBytes: 4, contexts: 2}}
		docs := s.load(reviewContext{Scope: scope, Goal: goal})

		assert.Equal(t, "1234", docs[0].Content)
		assert.Empty(t, docs[1].Content)
		assert.Contains(t, docs[1].Notice, "snapshot display limit was reached")
	})
}

func TestInputSnapshotter_unsafeAndUnusualInputs(t *testing.T) {
	t.Run("terminal controls are never displayed", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("safe\x1b[31munsafe"), 0o600))

		docs := (&inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 100, contexts: 2}}).
			load(reviewContext{Scope: scope})

		assert.Empty(t, docs[0].Content)
		assert.Contains(t, docs[0].Notice, "binary or unsafe text")
		assert.NotContains(t, docs[0].Notice, "\x1b")
	})

	t.Run("CRLF is normalized and a bare carriage return is rejected", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("first\r\nsecond"), 0o600))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 100, contexts: 2}}
		docs := s.load(reviewContext{Scope: scope})
		assert.Equal(t, "first\nsecond", docs[0].Content)

		require.NoError(t, os.WriteFile(scope, []byte("trusted\rspoofed"), 0o600))
		s = &inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 100, contexts: 2}}
		docs = s.load(reviewContext{Scope: scope})
		assert.Empty(t, docs[0].Content)
		assert.Contains(t, docs[0].Notice, "binary or unsafe text")
	})

	t.Run("truncation before the newline keeps a CRLF document visible", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("12345\r\nlast"), 0o600))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 6, totalBytes: 100, contexts: 2}}
		docs := s.load(reviewContext{Scope: scope})

		assert.Equal(t, "12345", docs[0].Content)
		assert.Equal(t, "truncated: showing 5 of 11 bytes", docs[0].Notice)
	})

	t.Run("file symlinks are read and directory symlinks are not traversed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires privileges on Windows")
		}
		root := t.TempDir()
		contextDir, targetDir := filepath.Join(root, "context"), filepath.Join(root, "target-dir")
		require.NoError(t, os.Mkdir(contextDir, 0o750))
		require.NoError(t, os.Mkdir(targetDir, 0o750))
		target := filepath.Join(root, "target.txt")
		require.NoError(t, os.WriteFile(target, []byte("linked text"), 0o600))
		require.NoError(t, os.Symlink(target, filepath.Join(contextDir, "file-link.txt")))
		require.NoError(t, os.Symlink(targetDir, filepath.Join(contextDir, "dir-link")))
		require.NoError(t, os.Symlink(filepath.Join(root, "missing"), filepath.Join(contextDir, "broken")))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 1000, contexts: 10}}
		docs := s.context(contextDir)

		require.Len(t, docs, 3)
		assert.Equal(t, "broken", docs[0].Label)
		assert.Contains(t, docs[0].Notice, "cannot read")
		assert.Equal(t, "dir-link/", docs[1].Label)
		assert.Equal(t, "symlinked directory not traversed", docs[1].Notice)
		assert.Equal(t, "linked text", docs[2].Content)
	})

	t.Run("a symlinked context root is listed but not traversed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires privileges on Windows")
		}
		root := t.TempDir()
		targetDir, contextLink := filepath.Join(root, "target-dir"), filepath.Join(root, "context")
		require.NoError(t, os.Mkdir(targetDir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(targetDir, "design.md"), []byte("hidden"), 0o600))
		require.NoError(t, os.Symlink(targetDir, contextLink))

		docs := (&inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 1000, contexts: 10}}).
			context(contextLink)

		require.Len(t, docs, 1)
		assert.Equal(t, "context/", docs[0].Label)
		assert.Equal(t, "input/context", docs[0].Path)
		assert.Equal(t, "symlinked directory not traversed", docs[0].Notice)
	})
}
