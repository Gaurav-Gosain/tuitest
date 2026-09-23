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
// data, runtime, and the config and data search lists). That keeps the user's
// real tuios daemon socket, sessions, and config file completely untouched, and
// lets tests run in parallel without colliding on a shared daemon. See
// hermeticEnv for the two details that are easy to get wrong.
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
//     enterTerminalMode.
//
//  2. tuios shows mode changes as toast notifications that linger and stack, so
//     both "Terminal mode" and "Window management mode" can be on screen at
//     once. Assertions therefore wait for the *newly appearing* toast (a
//     positive edge) rather than for an old one to disappear.
//
// These tests assert on text tuios draws, so a tuios release that rewords its
// welcome screen or its toasts breaks them. When that happens, update the
// strings below from what `tuitest snap -- tuios` prints.
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
	welcomeHint = "new window"

	terminalModeToast = "Terminal mode"
	windowModeToast   = "Window management mode"
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

// maxSocketPath is the size of sockaddr_un's path field on Darwin, the smaller
// of the two Unix platforms tuitest runs on (Linux allows 108).
const maxSocketPath = 104

// hermeticEnv returns the environment for an isolated tuios installation: every
// XDG directory redirected into a fresh per-test root, and a plain POSIX shell.
//
// Two details are easy to get wrong:
//
//   - XDG_RUNTIME_DIR, where the daemon puts its unix socket, cannot live under
//     t.TempDir on macOS. That path starts with a 49 byte $TMPDIR and adds the
//     test name, and a socket path longer than 104 bytes makes bind fail with
//     EINVAL, which tuios reports as a daemon that "exited immediately". The
//     runtime directory is made under /tmp instead.
//   - XDG_CONFIG_DIRS and XDG_DATA_DIRS are search lists. On macOS they include
//     ~/.config whatever XDG_CONFIG_HOME says, so leaving them unset lets the
//     developer's own config into the test.
func hermeticEnv(t *testing.T) (env []string, runtimeDir string) {
	t.Helper()

	runtimeDir, err := os.MkdirTemp("/tmp", "tt")
	if err != nil {
		t.Fatalf("hermeticEnv: runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	if n := len(runtimeDir + "/tuios/tuios.sock.pid"); n > maxSocketPath {
		t.Fatalf("hermeticEnv: %s leaves a %d byte socket path, over the %d byte limit", runtimeDir, n, maxSocketPath)
	}
	env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)

	base := t.TempDir()
	for _, key := range []string{
		"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
		"XDG_CONFIG_DIRS", "XDG_DATA_DIRS",
	} {
		dir := filepath.Join(base, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("hermeticEnv: mkdir %s: %v", key, err)
		}
		env = append(env, key+"="+dir)
	}
	// A deterministic POSIX shell with no rc file changing the prompt.
	env = append(env, "SHELL=/bin/sh", "ENV=", "PS1=$ ")
	return env, runtimeDir
}

// startTuios spawns the standalone tuios TUI headlessly in a hermetic, per-test
// environment. TUIOS_NO_DAEMON keeps a bare "tuios" standalone, since recent
// releases otherwise start a daemon and attach to it. Animations are off because
// they make frames depend on timing without testing anything asserted here.
func startTuios(t *testing.T, opts ...tuitest.Option) *tuitest.Terminal {
	t.Helper()
	bin := locateTuios(t)
	env, _ := hermeticEnv(t)
	env = append(env, "TUIOS_NO_DAEMON=1")

	baseOpts := []tuitest.Option{
		tuitest.WithSize(120, 40),
		tuitest.WithTerm("xterm-256color"),
		tuitest.WithEnv(env...),
	}
	return tuitest.StartT(t, []string{bin, "--no-animations"}, append(baseOpts, opts...)...)
}

// enterTerminalMode presses 'i' until the terminal mode toast appears, then
// waits out the insert guard so the next typed command is not dropped. It
// retries because a keypress that lands while tuios is still settling can be
// swallowed.
func enterTerminalMode(t *testing.T, term *tuitest.Terminal) {
	t.Helper()
	for range 4 {
		if err := term.SendKeys("i"); err != nil {
			t.Fatal(err)
		}
		if err := term.WaitForText(terminalModeToast, 3*time.Second); err == nil {
			time.Sleep(insertGuard + 100*time.Millisecond)
			return
		}
		if _, exited := term.ExitCode(); exited {
			t.Fatalf("tuios exited while entering terminal mode\n%s", term.Snapshot())
		}
	}
	t.Fatalf("did not enter terminal mode\n%s", term.Snapshot())
}

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
	enterTerminalMode(t, term)

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
	if err := term.WaitForText(windowModeToast, 5*time.Second); err != nil {
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
	code, err := term.WaitExit(5 * time.Second)
	if err != nil {
		t.Fatalf("tuios did not exit after 'q': %v", err)
	}
	if code != 0 {
		t.Fatalf("tuios exited with code %d, want 0", code)
	}
}
