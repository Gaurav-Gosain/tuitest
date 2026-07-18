// Package tuios_test drives the real tuios terminal multiplexer through the
// tuitest harness, headlessly, to prove the harness works against a large,
// real-world TUI rather than only against the in-repo fixtures.
//
// The binary is located via TUIOS_BIN (an absolute path) or "tuios" on PATH;
// the tests skip when neither is present, so `go test ./...` stays green in a
// bare checkout. Build one with:
//
//	go build -o /tmp/tuios ./cmd/tuios   # from the tuios repo
//	TUIOS_BIN=/tmp/tuios go test ./examples/tuios/...
//
// Each test runs tuios in a fully isolated, hermetic environment: a private
// TERM, SHELL, and a per-test set of XDG directories (config, state, cache,
// data, runtime). That keeps the user's real tuios daemon socket, sessions, and
// config file completely untouched, and lets tests run in parallel without
// colliding on a shared daemon.
//
// # tuios interaction notes (learned by driving it)
//
// tuios boots into "window management mode": keystrokes are window-manager
// commands, not shell input. The lifecycle a test walks is:
//
//	n                       create a window (spawns $SHELL in a pane)
//	i                       enter "terminal mode" (keys now reach the shell)
//	<type a command>\r      run it in the pane
//	Alt+Esc                 leave terminal mode, back to window management
//	x                       close the focused window
//	q                       quit tuios
//
// Two behaviors matter for writing reliable assertions:
//
//  1. After entering terminal mode tuios suppresses unmodified single-character
//     keys for 150ms (a guard against misparsed mouse-sequence fragments during
//     the AllMotion->CellMotion transition). Input typed inside that window is
//     silently dropped, so tests settle briefly before typing. See
//     settlePastInsertGuard.
//
//  2. tuios shows mode changes as toast notifications that linger and stack, so
//     both "Terminal Mode" and "Window Management Mode" can be on screen at
//     once. Assertions therefore wait for the *newly appearing* toast (a
//     positive edge) rather than for an old one to disappear.
package tuios_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

const (
	welcomeText = "Terminal UI Operating System"
	welcomeHint = "Press 'n' for a new window"
)

// insertGuard is tuios's post-terminal-mode single-char suppression window
// (internal/input/keyboard_terminal.go). Settle a little past it before typing.
const insertGuard = 150 * time.Millisecond

// locateTuios returns the tuios binary path from TUIOS_BIN or PATH.
func locateTuios(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("TUIOS_BIN"); bin != "" {
		return bin
	}
	if bin, err := exec.LookPath("tuios"); err == nil {
		return bin
	}
	t.Skip("tuios binary not found: set TUIOS_BIN to an absolute path or put tuios on PATH")
	return ""
}

// startTuios spawns tuios headlessly in a hermetic, per-test environment. Every
// XDG directory is redirected into the test's TempDir so the real daemon socket,
// sessions, and user config are never touched, and TERM/SHELL are pinned for
// determinism.
func startTuios(t *testing.T, opts ...tuitest.Option) *tuitest.Terminal {
	t.Helper()
	bin := locateTuios(t)

	base := t.TempDir()
	env := make([]string, 0, 8)
	// Isolate config AND runtime (daemon socket lives under XDG_RUNTIME_DIR) plus
	// the rest of the XDG family, so nothing leaks into the user's real state.
	for _, key := range []string{
		"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
		"XDG_CACHE_HOME", "XDG_DATA_HOME",
	} {
		dir := filepath.Join(base, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("startTuios: mkdir %s: %v", key, err)
		}
		env = append(env, key+"="+dir)
	}
	// A deterministic POSIX shell with a stable, greppable prompt.
	env = append(env, "SHELL=/bin/sh")

	base_opts := []tuitest.Option{
		tuitest.WithSize(120, 40),
		tuitest.WithTerm("xterm-256color"),
		tuitest.WithEnv(env...),
	}
	return tuitest.StartT(t, []string{bin}, append(base_opts, opts...)...)
}

// settlePastInsertGuard waits out tuios's 150ms single-char suppression window
// so the next typed command is not silently dropped.
func settlePastInsertGuard() { time.Sleep(insertGuard + 100*time.Millisecond) }

// TestBootRendersWelcome proves the harness spawns tuios in a PTY, interprets
// its output, and sees the initial welcome UI.
func TestBootRendersWelcome(t *testing.T) {
	t.Parallel()
	term := startTuios(t)

	if err := term.WaitForText(welcomeText, 10*time.Second); err != nil {
		t.Fatalf("welcome banner never rendered: %v", err)
	}
	if err := term.WaitForText(welcomeHint, 5*time.Second); err != nil {
		t.Fatalf("welcome hint never rendered: %v", err)
	}
	if _, exited := term.ExitCode(); exited {
		t.Fatal("tuios exited during boot")
	}
}

// TestWindowLifecycle walks the full happy path end to end: boot, create a
// window, run a real command in its shell and assert the output appears, close
// the window, and quit cleanly with exit code 0.
func TestWindowLifecycle(t *testing.T) {
	t.Parallel()
	term := startTuios(t)

	// 1. Boot.
	if err := term.WaitForText(welcomeText, 10*time.Second); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// 2. Create a window; the welcome screen is replaced by a window frame.
	if err := term.SendKeys("n"); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitFor(func(s tuitest.Screen) bool {
		return !strings.Contains(s.Text(), welcomeHint)
	}, 5*time.Second); err != nil {
		t.Fatalf("window did not appear after 'n': %v", err)
	}

	// 3. Enter terminal mode so keystrokes reach the shell.
	if err := term.SendKeys("i"); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("Terminal Mode", 5*time.Second); err != nil {
		t.Fatalf("did not enter terminal mode: %v", err)
	}
	settlePastInsertGuard()

	// 4. Run a command in the pane's shell. The marker is computed by the shell
	//    ($((21*2)) -> 42) so seeing "marker-42" proves the command actually ran,
	//    not merely that our keystrokes echoed.
	if err := term.SendKeys("echo marker-$((21*2))", tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("marker-42", 5*time.Second); err != nil {
		t.Fatalf("command output never appeared: %v", err)
	}

	// 5. Leave terminal mode (Alt+Esc is the direct shortcut for Ctrl+B Esc).
	if err := term.SendKeys(tuitest.Alt(tuitest.Esc)); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("Window Management Mode", 5*time.Second); err != nil {
		t.Fatalf("did not return to window management mode: %v", err)
	}

	// 6. Close the window; tuios falls back to the welcome screen.
	if err := term.SendKeys("x"); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText(welcomeHint, 5*time.Second); err != nil {
		t.Fatalf("window did not close: %v", err)
	}

	// 7. Quit cleanly.
	if err := term.SendKeys("q"); err != nil {
		t.Fatal(err)
	}
	code, err := term.Wait(5 * time.Second)
	if err != nil {
		t.Fatalf("tuios did not exit after 'q': %v", err)
	}
	if code != 0 {
		t.Fatalf("tuios exited with code %d, want 0", code)
	}
}
