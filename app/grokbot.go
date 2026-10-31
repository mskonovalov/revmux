package main

import (
	"os"
	"strings"
)

// runningOnGrokBot detects whether this process is running in the Grok Bot / Sand box environment.
// Reliable signals include HOME=/home/box, USER=box, and Sand box environment variables.
func runningOnGrokBot() bool {
	// Check for Sand box-specific environment variables
	if os.Getenv("SAND_BOX_CLUSTER") != "" || os.Getenv("SAND_BOX_ID") != "" || os.Getenv("SAND_BOX_STORE_ID") != "" {
		return true
	}

	// Check for box user and home directory
	home := os.Getenv("HOME")
	user := os.Getenv("USER")
	
	if home == "/home/box" || user == "box" {
		return true
	}

	return false
}

// grokBotDefaultProfile returns the default profile name for Grok Bot environments.
// This is "cursor-regular" instead of "comprehensive" to use Cursor multi-family models.
func grokBotDefaultProfile() string {
	return "cursor-regular"
}

// grokBotProfileAlias maps short profile names to their Cursor multi-family equivalents
// when running on Grok Bot. Only applies when the short name is explicitly requested.
func grokBotProfileAlias(name string) string {
	// Only apply aliases on Grok Bot
	if !runningOnGrokBot() {
		return name
	}

	// Map short names to cursor-* variants
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "light":
		return "cursor-light"
	case "regular":
		return "cursor-regular"
	case "deep":
		return "cursor-deep"
	default:
		return name
	}
}
