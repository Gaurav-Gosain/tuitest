package tuitest

import (
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// newIdleTerminal builds a Terminal with no child attached. WaitStable only
// touches the emulator, the timestamps and the condition variable, so this is
// enough to test its timing rules without spawning anything.
func newIdleTerminal(quiet time.Duration) *Terminal {
	t := &Terminal{
		cfg:      config{cols: 20, rows: 5, stabilize: quiet},
		emu:      emu.New(20, 5),
		exitCode: -1,
	}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// TestWaitStableWaitsOutTheWindowAfterInput pins the fix for the WaitStable
// footgun: with output long finished but input just sent, the quiet window has
// to be measured from the input, otherwise the wait returns immediately against
// a screen that cannot yet show any reaction.
func TestWaitStableWaitsOutTheWindowAfterInput(t *testing.T) {
	const quiet = 150 * time.Millisecond
	term := newIdleTerminal(quiet)

	term.mu.Lock()
	term.lastWrite = time.Now().Add(-time.Second) // output finished long ago
	term.lastInput = time.Now()                   // input sent just now
	term.mu.Unlock()

	start := time.Now()
	if err := term.WaitStable(2 * time.Second); err != nil {
		t.Fatalf("WaitStable: %v", err)
	}
	if elapsed := time.Since(start); elapsed < quiet {
		t.Errorf("WaitStable returned after %s, before the %s window elapsed from the last input", elapsed, quiet)
	}
}

// TestWaitStableReturnsPromptlyWhenIdle is the other half of the rule: with no
// input pending, a terminal that has been quiet longer than the window is
// stable straight away, so the fix above must not turn every WaitStable into a
// fixed sleep.
func TestWaitStableReturnsPromptlyWhenIdle(t *testing.T) {
	const quiet = 200 * time.Millisecond
	term := newIdleTerminal(quiet)

	term.mu.Lock()
	term.lastWrite = time.Now().Add(-time.Second)
	term.lastInput = time.Now().Add(-time.Second)
	term.mu.Unlock()

	start := time.Now()
	if err := term.WaitStable(2 * time.Second); err != nil {
		t.Fatalf("WaitStable: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= quiet {
		t.Errorf("WaitStable took %s on an already-quiet terminal, want well under %s", elapsed, quiet)
	}
}

// TestWaitStableRestartsOnLateOutput checks that output arriving during the
// window pushes the deadline out, which is what makes the wait mean "quiesced"
// rather than "a fixed delay after input".
func TestWaitStableRestartsOnLateOutput(t *testing.T) {
	const quiet = 120 * time.Millisecond
	term := newIdleTerminal(quiet)

	term.mu.Lock()
	term.lastWrite = time.Now().Add(-time.Second)
	term.lastInput = time.Now()
	term.mu.Unlock()

	go func() {
		time.Sleep(quiet / 2)
		term.onData([]byte("late output"))
	}()

	start := time.Now()
	if err := term.WaitStable(2 * time.Second); err != nil {
		t.Fatalf("WaitStable: %v", err)
	}
	if elapsed := time.Since(start); elapsed < quiet+quiet/2 {
		t.Errorf("WaitStable returned after %s; output at %s should have restarted the %s window",
			elapsed, quiet/2, quiet)
	}
}

// TestEveryInputPathMarksActivity checks that all four ways of driving the
// child record input, since WaitStable's window is measured from that timestamp.
// A path that forgot to record it would silently reintroduce the stale-frame
// bug for exactly the tests that use it.
func TestEveryInputPathMarksActivity(t *testing.T) {
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}
	term, err := Start([]string{cat}, WithSize(20, 5))
	if err != nil {
		t.Fatalf("spawn cat: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })

	cases := []struct {
		name string
		send func() error
	}{
		{"SendKeys", func() error { return term.SendKeys("x") }},
		{"Type", func() error { return term.Type("y") }},
		{"SendMouse", func() error { return term.SendMouse(MouseEvent{Button: MouseLeft, Action: MousePress}) }},
		{"Resize", func() error { return term.Resize(30, 8) }},
	}
	for _, tc := range cases {
		stale := time.Now().Add(-time.Hour)
		term.mu.Lock()
		term.lastInput = stale
		term.mu.Unlock()

		if err := tc.send(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		term.mu.Lock()
		marked := term.lastInput
		term.mu.Unlock()
		if !marked.After(stale) {
			t.Errorf("%s did not record that input was sent", tc.name)
		}
	}
}

// TestWaitExitReportsTheCodeItWaitedFor spawns a program with a known non-zero
// exit repeatedly. Waking on the process handle instead of the terminal's own
// state used to return -1 whenever the wait won the race against the callback
// that records the code, which showed up as a rare flake in CI. The
// deterministic guard for that ordering lives in internal/ptyproc; this is the
// end-to-end assertion that the code a caller sees is the code the child had.
func TestWaitExitReportsTheCodeItWaitedFor(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	for i := 0; i < 50; i++ {
		term, err := Start([]string{sh, "-c", "exit 3"}, WithSize(20, 5))
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		code, err := term.WaitExit(10 * time.Second)
		_ = term.Close()
		if err != nil {
			t.Fatalf("iteration %d: WaitExit: %v", i, err)
		}
		if code != 3 {
			t.Fatalf("iteration %d: exit code = %d, want 3", i, code)
		}
	}
}

// TestWaitErrorsUnwrapToSentinels covers the errors.Is contract the README
// documents, so callers can branch on the kind of failure without depending on
// the concrete error types.
func TestWaitErrorsUnwrapToSentinels(t *testing.T) {
	var timeout error = &TimeoutError{Op: "WaitForText", Want: "text", Elapsed: time.Second}
	if !errors.Is(timeout, ErrTimeout) {
		t.Error("a *TimeoutError should match ErrTimeout")
	}
	if errors.Is(timeout, ErrChildExited) {
		t.Error("a *TimeoutError should not match ErrChildExited")
	}

	var closed error = &ClosedError{Op: "WaitForText", Want: "text", ExitCode: 1}
	if !errors.Is(closed, ErrChildExited) {
		t.Error("a *ClosedError should match ErrChildExited")
	}
	if errors.Is(closed, ErrTimeout) {
		t.Error("a *ClosedError should not match ErrTimeout")
	}

	if err := errSemanticDisabled("WaitForPrompt"); !errors.Is(err, ErrSemanticMarkers) {
		t.Errorf("the semantic-markers error should match ErrSemanticMarkers, got %v", err)
	}
}
