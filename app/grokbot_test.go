package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunningOnGrokBot(t *testing.T) {
	tests := []struct {
		name        string
		home        string
		user        string
		cluster     string
		sandboxID   string
		storeID     string
		wantGrokBot bool
	}{
		{
			name:        "detected via HOME=/home/box",
			home:        "/home/box",
			user:        "other",
			wantGrokBot: true,
		},
		{
			name:        "detected via USER=box",
			home:        "/home/other",
			user:        "box",
			wantGrokBot: true,
		},
		{
			name:        "detected via SAND_BOX_CLUSTER",
			home:        "/home/other",
			user:        "other",
			cluster:     "us-east-1",
			wantGrokBot: true,
		},
		{
			name:        "detected via SAND_BOX_ID",
			home:        "/home/other",
			user:        "other",
			sandboxID:   "sb-12345",
			wantGrokBot: true,
		},
		{
			name:        "detected via SAND_BOX_STORE_ID",
			home:        "/home/other",
			user:        "other",
			storeID:     "store-67890",
			wantGrokBot: true,
		},
		{
			name:        "not Grok Bot - regular user",
			home:        "/home/alice",
			user:        "alice",
			wantGrokBot: false,
		},
		{
			name:        "not Grok Bot - no indicators",
			home:        "/Users/dev",
			user:        "dev",
			wantGrokBot: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.home != "" {
				t.Setenv("HOME", tt.home)
			}
			if tt.user != "" {
				t.Setenv("USER", tt.user)
			}
			if tt.cluster != "" {
				t.Setenv("SAND_BOX_CLUSTER", tt.cluster)
			}
			if tt.sandboxID != "" {
				t.Setenv("SAND_BOX_ID", tt.sandboxID)
			}
			if tt.storeID != "" {
				t.Setenv("SAND_BOX_STORE_ID", tt.storeID)
			}

			got := runningOnGrokBot()
			assert.Equal(t, tt.wantGrokBot, got)
		})
	}
}

func TestGrokBotDefaultProfile(t *testing.T) {
	assert.Equal(t, "cursor-regular", grokBotDefaultProfile())
}

func TestGrokBotProfileAlias(t *testing.T) {
	tests := []struct {
		name      string
		isGrokBot bool
		input     string
		want      string
	}{
		{
			name:      "light -> cursor-light on Grok Bot",
			isGrokBot: true,
			input:     "light",
			want:      "cursor-light",
		},
		{
			name:      "regular -> cursor-regular on Grok Bot",
			isGrokBot: true,
			input:     "regular",
			want:      "cursor-regular",
		},
		{
			name:      "deep -> cursor-deep on Grok Bot",
			isGrokBot: true,
			input:     "deep",
			want:      "cursor-deep",
		},
		{
			name:      "Light -> cursor-light on Grok Bot (case insensitive)",
			isGrokBot: true,
			input:     "Light",
			want:      "cursor-light",
		},
		{
			name:      "comprehensive unchanged on Grok Bot",
			isGrokBot: true,
			input:     "comprehensive",
			want:      "comprehensive",
		},
		{
			name:      "focused unchanged on Grok Bot",
			isGrokBot: true,
			input:     "focused",
			want:      "focused",
		},
		{
			name:      "light unchanged off Grok Bot",
			isGrokBot: false,
			input:     "light",
			want:      "light",
		},
		{
			name:      "regular unchanged off Grok Bot",
			isGrokBot: false,
			input:     "regular",
			want:      "regular",
		},
		{
			name:      "deep unchanged off Grok Bot",
			isGrokBot: false,
			input:     "deep",
			want:      "deep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.isGrokBot {
				t.Setenv("HOME", "/home/box")
			} else {
				t.Setenv("HOME", "/home/alice")
			}

			got := grokBotProfileAlias(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
