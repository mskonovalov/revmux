package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userSettings points CLAUDE_CONFIG_DIR at a temp dir holding body as settings.json, so no test reads
// the developer's own Claude settings.
func userSettings(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
}

func TestSnapshotClaudeSettings_copiesOnlyListedKeys(t *testing.T) {
	userSettings(t, `{"apiKeyHelper":"/bin/key-helper","env":{"ANTHROPIC_BASE_URL":"https://gw"},"hooks":{"Stop":[]},"model":"opus"}`)
	o := options{ClaudeUserSettings: " apiKeyHelper, env ,"}

	path, err := o.snapshotClaudeSettings(t.TempDir())
	require.NoError(t, err)
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiKeyHelper":"/bin/key-helper","env":{"ANTHROPIC_BASE_URL":"https://gw"}}`, string(data),
		"hooks and model stay out: only the listed keys reach the agents")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the env block can carry gateway headers")
}

func TestSnapshotClaudeSettings_noKeysNoFile(t *testing.T) {
	path, err := options{}.snapshotClaudeSettings(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, path)
}

func TestSnapshotClaudeSettings_failsClosed(t *testing.T) {
	for _, tt := range []struct{ name, body, keys, want string }{
		{"missing key", `{"env":{}}`, "apiKeyHelper,env", `no "apiKeyHelper" key`},
		{"invalid JSON", `{not json`, "env", "parse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			userSettings(t, tt.body)
			_, err := options{ClaudeUserSettings: tt.keys}.snapshotClaudeSettings(t.TempDir())
			require.ErrorContains(t, err, tt.want)
		})
	}
	t.Run("missing file", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		_, err := options{ClaudeUserSettings: "env"}.snapshotClaudeSettings(t.TempDir())
		require.ErrorContains(t, err, "claude-user-settings")
	})
}
