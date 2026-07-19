package ptyproc

import (
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// TestDoneClosesAfterOnClose pins the ordering the exit-code path depends on:
// a waiter woken by Done must never observe a receiver that has not yet been
// handed the exit code. Closing Done first made tuitest's WaitExit report -1
// for a clean exit whenever it won that race.
func TestDoneClosesAfterOnClose(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}

	var handled atomic.Bool
	p, err := Start(Config{Argv: []string{sh, "-c", "exit 3"}, Cols: 20, Rows: 5}, Handler{
		OnClose: func(int) {
			// A real handler is fast; the sleep only widens the window so the
			// ordering is observable rather than probabilistic.
			time.Sleep(50 * time.Millisecond)
			handled.Store(true)
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("child never finished")
	}
	if !handled.Load() {
		t.Fatal("Done was closed before OnClose had returned")
	}
	if code, exited := p.ExitCode(); !exited || code != 3 {
		t.Fatalf("ExitCode() = (%d, %v), want (3, true)", code, exited)
	}
}
