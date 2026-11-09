package setting

import (
	"os"
	"testing"

	"github.com/dixieflatline76/Spice/v2/config"
)

// TestUserDirsAreSandboxed fails if this test binary could touch the real user
// cache/config/log directories (AGENTS.md §5.3): importing config redirects every
// per-user directory to a temp sandbox before any path is resolved.
func TestUserDirsAreSandboxed(t *testing.T) {
	if err := config.VerifyTestSandbox(); err != nil {
		t.Fatal(err)
	}
}

// TestMain removes this binary's user-dir sandbox when the tests finish.
func TestMain(m *testing.M) {
	code := m.Run()
	config.RemoveTestSandbox()
	os.Exit(code)
}
