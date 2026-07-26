package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor/mocks"
)

func TestPrintVersion(t *testing.T) {
	tests := []struct {
		name string
		rev  string
		want string
	}{
		{name: "stamped revision", rev: "master-abc1234-20260726T120000", want: "revmux master-abc1234-20260726T120000\n"},
		{name: "default revision", rev: revision, want: "revmux unknown\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			require.NoError(t, printVersion(buf, tt.rev))
			assert.Equal(t, tt.want, buf.String())
		})
	}

	t.Run("write failure", func(t *testing.T) {
		err := printVersion(failingWriter{}, "v1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write version")
	})
}

func TestBinary_versionOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "revmux")
	build := exec.Command("go", "build", "-ldflags", "-X main.revision=test-rev", "-o", bin, ".") //nolint:gosec // fixed argv, output path from t.TempDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	out, err = exec.Command(bin, "--version").CombinedOutput() //nolint:gosec // binary just built by this test
	require.NoError(t, err)
	assert.Equal(t, "revmux test-rev\n", string(out))
}

func TestRun_metaCommands(t *testing.T) {
	t.Run("version goes to stdout", func(t *testing.T) {
		r := newRunOpts(t, options{Version: true})
		assert.Equal(t, 0, run(r.opts()))
		assert.Equal(t, "revmux unknown\n", r.stdout.String())
		assert.Empty(t, r.stderr.String())
	})

	t.Run("init writes the template and says so on stderr", func(t *testing.T) {
		dir := isolate(t)
		r := newRunOpts(t, options{Init: true})
		assert.Equal(t, 0, run(r.opts()))
		assert.FileExists(t, filepath.Join(dir, projectDirName, configFileName))
		assert.Empty(t, r.stdout.String(), "stdout carries the report and nothing else")
		assert.Contains(t, r.stderr.String(), "wrote")
	})

	t.Run("dump-defaults extracts the tree", func(t *testing.T) {
		dir := t.TempDir()
		r := newRunOpts(t, options{DumpDefaults: dir})
		assert.Equal(t, 0, run(r.opts()))
		assert.FileExists(t, filepath.Join(dir, "lenses", "bugs.md"))
		assert.Empty(t, r.stdout.String())
	})

	t.Run("dump-defaults failure exits 2", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		blocked := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(blocked, 0o500))
		r := newRunOpts(t, options{DumpDefaults: filepath.Join(blocked, "out")})
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "error:")
	})

	t.Run("version write failure exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{Version: true})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write version")
	})
}

func TestRun_reviewGate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pr-1")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, scopeFileName), []byte("review the diff"), 0o600))

	tests := []struct {
		name    string
		opts    options
		want    int
		wantErr string
	}{
		{name: "resolves", opts: options{Task: "pr-1", TasksDir: root, Profile: "focused"}, want: 0},
		{name: "no task", opts: options{TasksDir: root, Profile: "focused"}, want: 2, wantErr: "--task is required"},
		{name: "bad task name", opts: options{Task: "../out", TasksDir: root, Profile: "focused"}, want: 2, wantErr: "path separator"},
		{name: "bad run name", opts: options{Task: "pr-1", Run: "a/b", TasksDir: root, Profile: "focused"}, want: 2, wantErr: "--run"},
		{name: "missing task dir", opts: options{Task: "pr-2", TasksDir: root, Profile: "focused"}, want: 2, wantErr: "task directory"},
		{name: "unknown profile", opts: options{Task: "pr-1", TasksDir: root, Profile: "nope"}, want: 2, wantErr: "resolve profile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunOpts(t, tt.opts)
			assert.Equal(t, tt.want, run(r.opts()))
			assert.Empty(t, r.stdout.String(), "nothing but the report may reach stdout")
			if tt.wantErr != "" {
				assert.Contains(t, r.stderr.String(), tt.wantErr)
				return
			}
			assert.Empty(t, r.stderr.String())
		})
	}
}

// runHarness holds the writers a run wrote to, so a test can assert on each stream separately.
type runHarness struct {
	o      options
	stdout *strings.Builder
	stderr *strings.Builder
}

func newRunOpts(t *testing.T, o options) *runHarness {
	t.Helper()
	return &runHarness{o: o, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
}

func (r *runHarness) opts() runOpts {
	clk := &mocks.ClockMock{NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) }}
	return runOpts{
		opts: r.o, clock: clk, stdout: r.stdout, stderr: r.stderr,
		openTTY: func() (*os.File, error) { return nil, errors.New("no tty in tests") },
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
