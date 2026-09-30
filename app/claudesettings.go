package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeUserSettingsKeys splits the claude-user-settings knob into the top-level keys it names.
func (o options) claudeUserSettingsKeys() []string {
	return commaList(o.ClaudeUserSettings)
}

// commaList splits a comma-separated knob into its trimmed, non-empty items.
func commaList(value string) []string {
	var items []string
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// snapshotClaudeSettings copies the configured top-level keys of the user's Claude settings into a
// 0600 file under dir and returns its path, or "" when no keys are configured. Agents run with
// --setting-sources project, so this is how a user-only apiKeyHelper and its env reach them without
// the rest of the user layer. The file is read on every call rather than once: logging in to a
// gateway rewrites these keys, so the reviewers' copy is taken after the auth gate. A missing key
// fails the run, because an agent without the helper would silently fall back to other credentials.
func (o options) snapshotClaudeSettings(dir string) (string, error) {
	keys := o.claudeUserSettingsKeys()
	if len(keys) == 0 {
		return "", nil
	}
	src := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("claude-user-settings: %w", err)
		}
		src = filepath.Join(home, ".claude", "settings.json")
	}
	data, err := os.ReadFile(src) //nolint:gosec // the user's own Claude settings, located the way Claude locates them
	if err != nil {
		return "", fmt.Errorf("claude-user-settings: %w", err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(data, &all); err != nil {
		return "", fmt.Errorf("claude-user-settings: parse %s: %w", src, err)
	}
	picked := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		value, ok := all[key]
		if !ok {
			return "", fmt.Errorf("claude-user-settings: %s has no %q key", src, key)
		}
		picked[key] = value
	}
	out, err := json.Marshal(picked)
	if err != nil {
		return "", fmt.Errorf("claude-user-settings: %w", err)
	}
	f, err := os.CreateTemp(dir, "claude-settings-*.json") // CreateTemp opens 0600
	if err != nil {
		return "", fmt.Errorf("claude-user-settings: %w", err)
	}
	if _, err = f.Write(out); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("claude-user-settings: write %s: %w", f.Name(), err)
	}
	if err = f.Close(); err != nil {
		return "", fmt.Errorf("claude-user-settings: write %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}
