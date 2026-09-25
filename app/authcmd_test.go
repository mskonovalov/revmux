package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
)

func TestAuthCommand(t *testing.T) {
	t.Run("parses without a task or round", func(t *testing.T) {
		o, err := parseArgs([]string{"--config-dir", isolate(t), "auth", "--profile", "focused"})
		require.NoError(t, err)
		assert.True(t, o.showAuth)
		assert.Empty(t, o.Task)
		assert.Empty(t, o.Run)
	})

	t.Run("checks providers without touching a task or report stdout", func(t *testing.T) {
		r := newRunOpts(t, options{showAuth: true, Profile: "focused", TasksDir: t.TempDir()})
		ro := r.opts()
		checked := map[string]int{}
		ro.newAuth = func(name string) executor.Authenticator {
			return authProviderMock{
				status: func() (bool, error) { checked[name]++; return true, nil },
				login:  func(io.ReadWriter) error { t.Fatal("already authenticated"); return nil },
			}
		}
		assert.Equal(t, 0, run(ro))
		assert.Equal(t, map[string]int{"claude": 1, "codex": 1}, checked)
		assert.Empty(t, r.stdout.String())
		assert.Equal(t, []string{"."}, treeOf(t, r.o.TasksDir))
	})

	t.Run("a login failure stops before creating any task", func(t *testing.T) {
		r := newRunOpts(t, options{showAuth: true, Profile: "focused", TasksDir: t.TempDir()})
		ro := r.opts()
		ro.newAuth = func(name string) executor.Authenticator {
			return authProviderMock{
				status: func() (bool, error) { return name != "claude", nil },
				login:  func(io.ReadWriter) error { return assert.AnError },
			}
		}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "claude")
		assert.Equal(t, []string{"."}, treeOf(t, r.o.TasksDir))
	})
}
