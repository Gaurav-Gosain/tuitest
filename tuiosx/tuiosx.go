// Package tuiosx holds the tuios-specific conveniences for tuitest: a prefix
// chord helper and a spawn helper that isolates each tuios instance in its own
// temporary XDG state so parallel tests do not collide on a shared daemon
// socket. Nothing in tuitest's core depends on this package; it can be deleted
// without touching the harness.
package tuiosx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Gaurav-Gosain/tuitest"
)

// Prefix sends the tuios leader chord (Ctrl+B) followed by key, the shape most
// tuios window-management commands take (Ctrl+B then c to create a window,
// Ctrl+B then % to split, and so on).
func Prefix(term *tuitest.Terminal, key string) error {
	return term.SendKeys(tuitest.Ctrl('b'), key)
}

// Locate returns the tuios binary path, honoring the TUIOS_BIN environment
// variable and otherwise falling back to PATH. ok is false when no binary is
// found.
func Locate() (path string, ok bool) {
	if bin := os.Getenv("TUIOS_BIN"); bin != "" {
		return bin, true
	}
	if bin, err := exec.LookPath("tuios"); err == nil {
		return bin, true
	}
	return "", false
}

// StartTuios spawns tuios in an isolated, hermetic environment for a test. It
// skips the test when no tuios binary is available. Each instance gets its own
// temporary XDG directories, so its daemon socket and state never collide with
// another test's, and the harness's process-group teardown reaps the daemon and
// any pane processes on Close.
func StartTuios(tb testing.TB, opts ...tuitest.Option) *tuitest.Terminal {
	tb.Helper()
	bin, ok := Locate()
	if !ok {
		tb.Skip("tuios binary not found (set TUIOS_BIN or add tuios to PATH)")
	}

	base := tb.TempDir()
	isoEnv := []string{}
	for _, name := range []string{
		"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME",
		"XDG_DATA_HOME", "XDG_RUNTIME_DIR",
	} {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatalf("tuiosx: create %s: %v", name, err)
		}
		isoEnv = append(isoEnv, name+"="+dir)
	}

	// Base options first, user options last so callers can override size/term.
	all := []tuitest.Option{
		tuitest.WithSize(120, 40),
		tuitest.WithEnv(isoEnv...),
	}
	all = append(all, opts...)
	return tuitest.StartT(tb, []string{bin}, all...)
}
