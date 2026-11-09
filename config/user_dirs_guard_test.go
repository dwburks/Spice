package config

import (
	"os"
	"testing"
)

// TestUserDirsAreSandboxed fails if this test binary could touch the real user
// cache/config/log directories (AGENTS.md §5.3): importing config redirects every
// per-user directory to a temp sandbox before any path is resolved.
func TestUserDirsAreSandboxed(t *testing.T) {
	if err := VerifyTestSandbox(); err != nil {
		t.Fatal(err)
	}
}

// TestMain removes this binary's user-dir sandbox when the tests finish.
func TestMain(m *testing.M) {
	code := m.Run()
	RemoveTestSandbox()
	os.Exit(code)
}
