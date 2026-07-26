package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	out, err = exec.Command(bin).CombinedOutput() //nolint:gosec // binary just built by this test
	require.NoError(t, err)
	assert.Equal(t, "revmux test-rev\n", string(out))
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
