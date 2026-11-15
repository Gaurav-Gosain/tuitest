package tuitest

import (
	"sync/atomic"
	"testing"
	"time"
)

// quietProgram prints "up" and then neither reads nor writes. Echo is off, so
// input sent to it produces no output either.
var quietProgram = []string{"/bin/sh", "-c", "stty -echo; echo up; exec sleep 30"}

// A screen condition can only change when something happens to the terminal,
// so a wait with nothing happening must not keep re-checking it. Before the
// fix every wait re-checked every 5 ms: about 100 calls in this half second.
func TestWaitDoesNotPollWithoutOutput(t *testing.T) {
	term := StartT(t, quietProgram, WithKillGrace(-1))
	if err := term.WaitForText("up", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	start := time.Now()
	err := term.WaitFor(func(Screen) bool { calls.Add(1); return false }, 500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("WaitFor returned nil for a condition that never holds")
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("WaitFor gave up after %s, before its timeout", elapsed)
	}
	// One call at the start and one at the deadline.
	if n := calls.Load(); n > 3 {
		t.Fatalf("the condition ran %d times in %s with no output; want at most 3", n, elapsed.Round(time.Millisecond))
	}
}

// Resize changes the screen without any output from the program, so it has to
// wake the waits itself.
func TestWaitWakesOnResize(t *testing.T) {
	term := StartT(t, quietProgram, WithKillGrace(-1))
	if err := term.WaitForText("up", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- term.WaitForDesc("the screen to be 100 columns wide", func(s Screen) bool {
			cols, _ := s.Size()
			return cols == 100
		}, 5*time.Second)
	}()
	time.Sleep(50 * time.Millisecond) // let the wait block first
	start := time.Now()
	if err := term.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the wait saw the resize after %s", d)
	}
}

// InputBytes is documented as safe to read in a condition. Input the program
// swallows produces no output, so sending it has to wake the waits.
func TestWaitWakesOnInput(t *testing.T) {
	term := StartT(t, quietProgram, WithKillGrace(-1))
	if err := term.WaitForText("up", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	base := term.InputBytes()
	done := make(chan error, 1)
	go func() {
		done <- term.WaitForDesc("five more bytes of input", func(Screen) bool {
			return term.InputBytes() >= base+5
		}, 5*time.Second)
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if err := term.Type("hello"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the wait saw the input after %s", d)
	}
}

// WaitStable still ends on time with no event to wake it: its quiet window is
// the one condition that turns true by the clock alone.
func TestWaitStableEndsWithoutAnEvent(t *testing.T) {
	term := StartT(t, quietProgram, WithKillGrace(-1), WithStabilizeInterval(100*time.Millisecond))
	if err := term.WaitForText("up", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("x"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := term.WaitStable(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 90*time.Millisecond || d > time.Second {
		t.Fatalf("WaitStable returned after %s; want about 100ms", d)
	}
}
