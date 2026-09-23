package tuitest_test

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// startProbe runs testdata/rawprobe, which writes setup verbatim and then shows
// every input byte it receives in hex on its second row.
func startProbe(t *testing.T, setup string, opts ...tuitest.Option) *tuitest.Terminal {
	t.Helper()
	term := tuitest.StartT(t, []string{probeBin, setup}, opts...)
	if err := term.WaitForText("READY", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return term
}

// hexOf renders s the way rawprobe prints it.
func hexOf(s string) string {
	parts := make([]string, len(s))
	for i := 0; i < len(s); i++ {
		parts[i] = fmt.Sprintf("%02x", s[i])
	}
	return strings.Join(parts, " ")
}

// TestArrowKeysReachAProgramInApplicationCursorMode checks the bytes on the
// wire. Verified to fail without the fix: SendKeys sent ESC [ A whatever the
// mode, and the probe reported 1b 5b 41.
func TestArrowKeysReachAProgramInApplicationCursorMode(t *testing.T) {
	t.Parallel()
	term := startProbe(t, "\x1b[?1h")
	if err := term.SendKeys(tuitest.Up, tuitest.Home); err != nil {
		t.Fatal(err)
	}
	want := "in: " + hexOf("\x1bOA\x1bOH")
	if err := term.WaitForText(want, 5*time.Second); err != nil {
		t.Fatalf("a program that set DECCKM should receive SS3 cursor keys: %v", err)
	}
}

// TestArrowKeysInNormalCursorMode is the control: without mode 1 the keys go
// out as CSI sequences, exactly as their constants say.
func TestArrowKeysInNormalCursorMode(t *testing.T) {
	t.Parallel()
	term := startProbe(t, "")
	if err := term.SendKeys(tuitest.Up, tuitest.Home); err != nil {
		t.Fatal(err)
	}
	want := "in: " + hexOf("\x1b[A\x1b[H")
	if err := term.WaitForText(want, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestResizeSendsTheInBandReport covers mode 2048. The emulator queues the
// report when the grid is resized, and it used to sit in the queue until the
// program next wrote something, which a program waiting for the report never
// does. Verified to fail without the fix: the probe saw the report sent when
// the mode was enabled and nothing after the resize.
func TestResizeSendsTheInBandReport(t *testing.T) {
	t.Parallel()
	// Wide enough that the hex of both reports fits on one row.
	term := startProbe(t, "\x1b[?2048h", tuitest.WithSize(200, 10))
	// Enabling the mode sends the current size straight away.
	if err := term.WaitForText(hexOf("\x1b[48;10;200;0;0t"), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(210, 12); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText(hexOf("\x1b[48;12;210;0;0t"), 5*time.Second); err != nil {
		t.Fatalf("the in-band resize report never reached the program: %v", err)
	}
}

// TestInputAfterExitWrapsErrChildExited checks that sending to a program that
// has gone says so, in the same terms a wait uses. Before, the error was the
// platform's: an I/O error on macOS, and on Linux often no error at all.
func TestInputAfterExitWrapsErrChildExited(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	term := tuitest.StartT(t, []string{sh, "-c", "exit 5"})
	if _, err := term.WaitExit(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	for name, send := range map[string]func() error{
		"SendKeys":  func() error { return term.SendKeys("x") },
		"Type":      func() error { return term.Type("x") },
		"Paste":     func() error { return term.Paste("x") },
		"SendMouse": func() error { return term.SendMouse(tuitest.MouseEvent{}) },
	} {
		err := send()
		if !errors.Is(err, tuitest.ErrChildExited) {
			t.Errorf("%s after exit returned %v, want an error wrapping ErrChildExited", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "code 5") {
			t.Errorf("%s after exit: the error should name the exit code, got %v", name, err)
		}
	}
}

// TestInputRacingAnExitWrapsErrChildExited sends input while the program is
// exiting. On macOS a write to a PTY whose program has just closed its end
// fails with EIO a moment before the pump reads end of file and records the
// exit, and that errno used to be the error, which the fuzzer then reported as
// a crash of a program that had simply quit.
func TestInputRacingAnExitWrapsErrChildExited(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	for i := 0; i < 20; i++ {
		term, err := tuitest.Start([]string{sh, "-c", "exit 0"})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			err := term.SendKeys("x")
			if err == nil {
				continue
			}
			if !errors.Is(err, tuitest.ErrChildExited) {
				t.Fatalf("iteration %d: input to an exiting program returned %v, want an error wrapping ErrChildExited", i, err)
			}
			break
		}
		_ = term.Close()
	}
}

// TestAProgramThatNeverReadsItsAnswersKeepsRunning floods the terminal with
// cursor position queries and never reads the answers. The answers used to be
// written from the output pump, so once the program's input buffer filled the
// pump blocked, stopped reading output, and the program blocked writing it: the
// marker never arrived and teardown could not even kill the child. Verified to
// fail without the fix.
func TestAProgramThatNeverReadsItsAnswersKeepsRunning(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	burst := strings.Repeat(`\033[6n`, 50)
	script := `stty raw; i=0; while [ $i -lt 200 ]; do printf '` + burst + `'; i=$((i+1)); done; echo FLOOD-DONE; sleep 30`
	term := tuitest.StartT(t, []string{sh, "-c", script}, tuitest.WithLog(nil))
	if err := term.WaitForText("FLOOD-DONE", 20*time.Second); err != nil {
		t.Fatalf("the output pump stalled behind unread query answers: %v", err)
	}
}

// TestWaitStableWaitsForASlowStarter spawns a program whose first byte comes
// later than the quiet window. WaitStable used to count the window from spawn,
// return before anything was drawn, and hand back a blank screen, which is how
// TestWaitStable flaked under -race.
func TestWaitStableWaitsForASlowStarter(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	term := tuitest.StartT(t, []string{sh, "-c", "sleep 0.3; printf 'late banner'; sleep 5"},
		tuitest.WithStabilizeInterval(50*time.Millisecond))
	if err := term.WaitStable(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := term.Snapshot(); got != "late banner" {
		t.Fatalf("WaitStable returned before the program drew anything: %q", got)
	}
}
