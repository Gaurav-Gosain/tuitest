package tuitest

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a log writer that the test can read while the pump writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Input sent after the program has exited is refused. The log must not show it
// as sent: a person reading a failed test's log would take it for input the
// program got.
func TestRefusedInputAfterExitIsNotLogged(t *testing.T) {
	var log lockedBuffer
	term := StartT(t, []string{"/bin/sh", "-c", "echo bye"}, WithLog(&log))
	if _, err := term.WaitExit(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	err := term.SendKeys("refused-after-exit")
	if !errors.Is(err, ErrChildExited) {
		t.Fatalf("SendKeys after exit: got %v, want an error wrapping ErrChildExited", err)
	}
	if strings.Contains(log.String(), "refused-after-exit") {
		t.Fatalf("the log shows refused input as sent:\n%q", log.String())
	}
}

// After a write times out the terminal refuses all input. The refused input
// must not reach the log, and must not restart WaitStable's quiet window.
func TestRefusedInputAfterStallIsNotLogged(t *testing.T) {
	var log lockedBuffer
	term := StartT(t, []string{"/bin/sh", "-c", "stty -echo; echo up; exec sleep 30"},
		WithLog(&log), WithWriteTimeout(200*time.Millisecond), WithKillGrace(-1),
		WithStabilizeInterval(100*time.Millisecond))
	if err := term.WaitForText("up", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Stand in for a write blocked on a full input buffer, so this one times
	// out and the terminal stops taking input.
	term.inputMu.Lock()
	err := term.Type("x")
	term.inputMu.Unlock()
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Type behind a blocked write: got %v, want a timeout", err)
	}
	if err := term.WaitStable(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("refused-after-stall"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Type after the stall: got %v, want the earlier timeout", err)
	}
	if strings.Contains(log.String(), "refused-after-stall") {
		t.Fatal("the log shows refused input as sent")
	}
	// The quiet window has already run out and the refused input must not
	// restart it, so WaitStable returns at once.
	start := time.Now()
	if err := term.WaitStable(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("WaitStable took %s after refused input; the refusal restarted the quiet window", d)
	}
}
