package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseArgs_GrokBotDefaultProfile(t *testing.T) {
	// Save original environment
	originalHome := os.Getenv("HOME")
	originalUser := os.Getenv("USER")
	
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USER", originalUser)
	}()

	t.Run("default profile is cursor-regular on Grok Bot", func(t *testing.T) {
		// Set Grok Bot environment
		os.Setenv("HOME", "/home/box")
		os.Setenv("USER", "box")

		dir := isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--tasks-dir", dir})
		require.NoError(t, err)

		assert.Equal(t, "cursor-regular", o.Profile)
		assert.Equal(t, originDefault, o.knobOrigins["profile"])
	})

	t.Run("default profile is comprehensive off Grok Bot", func(t *testing.T) {
		// Set regular environment
		os.Setenv("HOME", "/home/alice")
		os.Setenv("USER", "alice")

		dir := isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--tasks-dir", dir})
		require.NoError(t, err)

		assert.Equal(t, "comprehensive", o.Profile)
		assert.Equal(t, originDefault, o.knobOrigins["profile"])
	})

	t.Run("explicit --profile flag wins on Grok Bot", func(t *testing.T) {
		// Set Grok Bot environment
		os.Setenv("HOME", "/home/box")
		os.Setenv("USER", "box")

		dir := isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--tasks-dir", dir, "--profile", "focused"})
		require.NoError(t, err)

		assert.Equal(t, "focused", o.Profile)
		assert.Equal(t, originFlag, o.knobOrigins["profile"])
	})

	t.Run("INI profile setting wins on Grok Bot", func(t *testing.T) {
		// Set Grok Bot environment
		os.Setenv("HOME", "/home/box")
		os.Setenv("USER", "box")

		dir := isolate(t)
		user := filepath.Join(dir, "user")
		writeConfig(t, user, "profile = comprehensive\n")

		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.NoError(t, err)

		assert.Equal(t, "comprehensive", o.Profile)
		assert.Equal(t, originUser, o.knobOrigins["profile"])
	})
}

func TestPromptSet_GrokBotProfileAliases(t *testing.T) {
	// Save original environment
	originalHome := os.Getenv("HOME")
	originalUser := os.Getenv("USER")
	
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USER", originalUser)
	}()

	dir := isolate(t)

	tests := []struct {
		name           string
		isGrokBot      bool
		profileArg     string
		expectedResolved string
	}{
		{
			name:           "light resolves to cursor-light on Grok Bot",
			isGrokBot:      true,
			profileArg:     "light",
			expectedResolved: "cursor-light",
		},
		{
			name:           "regular resolves to cursor-regular on Grok Bot",
			isGrokBot:      true,
			profileArg:     "regular",
			expectedResolved: "cursor-regular",
		},
		{
			name:           "deep resolves to cursor-deep on Grok Bot",
			isGrokBot:      true,
			profileArg:     "deep",
			expectedResolved: "cursor-deep",
		},
		{
			name:           "focused unchanged on Grok Bot",
			isGrokBot:      true,
			profileArg:     "focused",
			expectedResolved: "focused",
		},
		{
			name:           "lite profile works off Grok Bot",
			isGrokBot:      false,
			profileArg:     "lite",
			expectedResolved: "lite",
		},
		{
			name:           "deep unchanged off Grok Bot (resolves to existing deep profile)",
			isGrokBot:      false,
			profileArg:     "deep",
			expectedResolved: "deep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			os.Unsetenv("HOME")
			os.Unsetenv("USER")
			os.Unsetenv("SAND_BOX_CLUSTER")

			if tt.isGrokBot {
				os.Setenv("HOME", "/home/box")
			} else {
				os.Setenv("HOME", "/home/alice")
			}

			o, err := parseArgs([]string{"--task", "pr-1", "--tasks-dir", dir, "--profile", tt.profileArg})
			require.NoError(t, err)

			// promptSet should resolve the profile successfully
			set, err := o.promptSet()
			require.NoError(t, err)

			// Verify the aliased profile is accessible
			profileName := grokBotProfileAlias(o.Profile)
			assert.Equal(t, tt.expectedResolved, profileName)

			profile, err := set.Profile(profileName)
			require.NoError(t, err)
			assert.NotNil(t, profile)
		})
	}
}

func TestCatalog_GrokBotReportsCorrectProfile(t *testing.T) {
	// Save original environment
	originalHome := os.Getenv("HOME")
	originalUser := os.Getenv("USER")
	
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USER", originalUser)
	}()

	t.Run("catalog reports cursor-regular default on Grok Bot", func(t *testing.T) {
		// Set Grok Bot environment
		os.Setenv("HOME", "/home/box")
		os.Setenv("USER", "box")

		dir := isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--tasks-dir", dir})
		require.NoError(t, err)

		set, err := o.promptSet()
		require.NoError(t, err)

		cat := o.catalog(set)

		got := map[string]knob{}
		for _, k := range cat.Knobs {
			got[k.Name] = k
		}

		assert.Equal(t, "cursor-regular", got["profile"].Value)
		assert.Equal(t, originDefault, got["profile"].Source)
	})

	t.Run("catalog reports comprehensive default off Grok Bot", func(t *testing.T) {
		// Set regular environment
		os.Setenv("HOME", "/home/alice")
		os.Setenv("USER", "alice")

		dir := isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--tasks-dir", dir})
		require.NoError(t, err)

		set, err := o.promptSet()
		require.NoError(t, err)

		cat := o.catalog(set)

		got := map[string]knob{}
		for _, k := range cat.Knobs {
			got[k.Name] = k
		}

		assert.Equal(t, "comprehensive", got["profile"].Value)
		assert.Equal(t, originDefault, got["profile"].Source)
	})
}
