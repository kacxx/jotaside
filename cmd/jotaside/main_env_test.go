package main

import (
	"os"
	"testing"
)

// TestMain points Claude Code's config directory at an empty one, so find
// never scans the transcripts of whoever runs the tests.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jotaside-claude-")
	if err != nil {
		panic(err)
	}
	os.Setenv("CLAUDE_CONFIG_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
