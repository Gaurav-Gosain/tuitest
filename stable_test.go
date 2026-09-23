package tuitest

import (
	"strings"
	"testing"
	"time"
)

// TestWaitForStableWaitsForTheRestOfTheFrame is the case WaitFor gets wrong:
// the marker is drawn before the rest of the frame, so a read made the moment
// it appears misses the rows that follow.
func TestWaitForStableWaitsForTheRestOfTheFrame(t *testing.T) {
	const quiet = 300 * time.Millisecond
	term := newIdleTerminal(quiet)

	go func() {
		term.onData([]byte("header\r\n"))
		time.Sleep(quiet / 4)
		term.onData([]byte("body\r\n"))
		time.Sleep(quiet / 4)
		term.onData([]byte("footer"))
	}()

	var seen Screen
	err := term.WaitForStable(func(s Screen) bool {
		seen = s
		return strings.Contains(s.Text(), "header")
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := seen.Text(); got != "header\nbody\nfooter" {
		t.Fatalf("WaitForStable returned on a frame that was still being drawn:\n%s", got)
	}
}

// TestWaitForStableIgnoresRepaintsOfTheSameContent checks that output which
// does not change the cells does not restart the window, so a program that
// redraws an identical frame on a timer still settles.
func TestWaitForStableIgnoresRepaintsOfTheSameContent(t *testing.T) {
	const quiet = 100 * time.Millisecond
	term := newIdleTerminal(quiet)
	term.onData([]byte("\x1b[Hsteady"))
	term.mu.Lock()
	term.lastWrite = time.Now().Add(-time.Second)
	term.lastInput = time.Now().Add(-time.Second)
	term.mu.Unlock()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				term.onData([]byte("\x1b[Hsteady"))
			}
		}
	}()

	// The repaints come every 10ms, well inside the window, so this can only
	// finish if they are not counted as changes.
	if err := term.WaitForStable(func(s Screen) bool { return s.Text() == "steady" }, 2*time.Second); err != nil {
		t.Fatalf("a screen repainted with identical content never counted as stable: %v", err)
	}
}

// TestWaitForStableWaitsOutTheWindowAfterInput applies WaitStable's input rule:
// a condition that already held before a keystroke is not reported until the
// program has had the interval to react.
func TestWaitForStableWaitsOutTheWindowAfterInput(t *testing.T) {
	const quiet = 100 * time.Millisecond
	term := newIdleTerminal(quiet)
	term.onData([]byte("ready"))
	term.mu.Lock()
	term.lastWrite = time.Now().Add(-time.Second)
	term.lastInput = time.Now()
	term.mu.Unlock()

	start := time.Now()
	if err := term.WaitForStable(func(s Screen) bool { return true }, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < quiet {
		t.Errorf("WaitForStable returned %s after input, before the %s window", elapsed, quiet)
	}
}

// TestWaitForStableAcceptsTheFinalScreenOfAnExitedChild checks that exit ends
// the wait when the condition holds, rather than failing it.
func TestWaitForStableAcceptsTheFinalScreenOfAnExitedChild(t *testing.T) {
	term := newIdleTerminal(time.Hour)
	term.onData([]byte("done"))
	term.onClose(0)
	if err := term.WaitForStable(func(s Screen) bool { return s.Text() == "done" }, time.Second); err != nil {
		t.Fatalf("WaitForStable on the final screen of an exited child: %v", err)
	}
	if err := term.WaitForStable(func(s Screen) bool { return false }, time.Second); err == nil {
		t.Fatal("WaitForStable should fail when the child exited without the condition holding")
	}
}
