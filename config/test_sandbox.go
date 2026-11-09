package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Test sandbox: inside a `go test` binary every per-user directory (home, cache, config,
// Windows AppData/LocalAppData/USERPROFILE) is redirected to a private temporary directory
// before anything resolves a path. This runs from this package's init, which precedes the
// log package's init (it opens Spice.log under GetAppDir) and every caller of
// GetWorkingDir / os.UserCacheDir / os.UserConfigDir, so no test can read or write the
// real Spice log, cache, favorites, lock file or Fyne preferences.
//
// Production binaries are unaffected: testing.Testing() is false outside test binaries.

// testSandboxPrefix names sandbox directories under os.TempDir().
const testSandboxPrefix = "spice-test-"

// staleTestSandboxAge is how old a leftover sandbox from an earlier test run must be
// before it is removed (younger ones may belong to a test binary still running).
const staleTestSandboxAge = 24 * time.Hour

var testSandboxDir string

// TestSandboxDir returns the sandbox root in test binaries, or "" in production.
func TestSandboxDir() string {
	return testSandboxDir
}

func setupTestSandbox() {
	if !testing.Testing() {
		return
	}
	root, err := os.MkdirTemp("", testSandboxPrefix)
	if err != nil {
		// Never fall back to the real user directories.
		panic(fmt.Sprintf("spice: cannot create test sandbox: %v", err))
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	sweepStaleTestSandboxes(root)

	home := filepath.Join(root, "home")
	dirs := map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,                                // Windows os.UserHomeDir
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg-cache"),    // Linux os.UserCacheDir
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),   // Linux os.UserConfigDir
		"XDG_DATA_HOME":   filepath.Join(root, "xdg-data"),     // Linux data (Fyne, others)
		"LocalAppData":    filepath.Join(root, "localappdata"), // Windows os.UserCacheDir
		"AppData":         filepath.Join(root, "appdata"),      // Windows os.UserConfigDir
	}
	for key, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(fmt.Sprintf("spice: cannot create test sandbox dir %s: %v", dir, err))
		}
		if err := os.Setenv(key, dir); err != nil {
			panic(fmt.Sprintf("spice: cannot set %s for test sandbox: %v", key, err))
		}
	}
	testSandboxDir = root
}

// RemoveTestSandbox deletes this test binary's sandbox (best effort: on Windows a file still
// open, such as the log, can keep parts of it until the stale-sandbox sweep). No-op in
// production.
func RemoveTestSandbox() {
	if testSandboxDir != "" {
		_ = os.RemoveAll(testSandboxDir)
	}
}

// sweepStaleTestSandboxes removes sandboxes left behind by earlier test runs.
func sweepStaleTestSandboxes(current string) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), testSandboxPrefix) {
			continue
		}
		path := filepath.Join(os.TempDir(), e.Name())
		if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved == current {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTestSandboxAge {
			_ = os.RemoveAll(path)
		}
	}
}

// VerifyTestSandbox reports an error if any per-user directory Spice resolves points
// outside the test sandbox. Guard tests in each package call it.
func VerifyTestSandbox() error {
	root := TestSandboxDir()
	if root == "" {
		return fmt.Errorf("not running in a test sandbox")
	}
	check := func(name, path string, err error) error {
		if err != nil {
			return nil // directory not defined on this OS (e.g. no XDG on Windows)
		}
		if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
			path = resolved
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s resolves outside the test sandbox: %s (sandbox %s, %s)", name, path, root, runtime.GOOS)
		}
		return nil
	}
	cache, cacheErr := os.UserCacheDir()
	cfg, cfgErr := os.UserConfigDir()
	home, homeErr := os.UserHomeDir()
	for _, c := range []struct {
		name string
		path string
		err  error
	}{
		{"os.UserCacheDir", cache, cacheErr},
		{"os.UserConfigDir", cfg, cfgErr},
		{"os.UserHomeDir", home, homeErr},
		{"config.GetWorkingDir", GetWorkingDir(), nil},
		{"config.GetAppDir", GetAppDir(), nil},
	} {
		if err := check(c.name, c.path, c.err); err != nil {
			return err
		}
	}
	return nil
}
