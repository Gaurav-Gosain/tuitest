package tuitest_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// TestExitCodeVisibleAsSoonAsDoneFires pins the invariant that the exit code is
// readable the moment the child is reported done. The process records its status
// before closing Done, but the terminal's own copy is filled in by a callback
// that runs afterwards, so a naive read in that window returns the "not exited"
// placeholder of -1 instead of the real code.
//
// The window is microseconds wide, so a single spawn would pass even against the
// bug. Repeating it puts enough scheduling pressure on the gap to expose a
// regression reliably; the loop is what gives this test its teeth.
func TestExitCodeVisibleAsSoonAsDoneFires(t *testing.T) {
	t.Parallel()

	const (
		workers    = 12
		perWorker  = 30
		spawnLimit = 5 * time.Second
	)

	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)

	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				// The input is sent without waiting for the banner: the PTY
				// buffers it, and skipping the round trip keeps each iteration
				// cheap. "boom" makes the fixture exit with status 3.
				term, err := tuitest.Start([]string{echoBin})
				if err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: spawn: %w", w, i, err)
					return
				}
				if err := term.SendKeys("boom", tuitest.Enter); err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: %w", w, i, err)
					_ = term.Close()
					return
				}

				select {
				case <-term.Done():
				case <-time.After(spawnLimit):
					errs <- fmt.Errorf("worker %d iteration %d: child never finished", w, i)
					_ = term.Close()
					return
				}

				code, exited := term.ExitCode()
				switch {
				case !exited:
					errs <- fmt.Errorf("worker %d iteration %d: ExitCode reports not exited after Done fired", w, i)
				case code != 3:
					errs <- fmt.Errorf("worker %d iteration %d: exit code = %d, want 3 (not published before Done)", w, i, code)
				}
				if err := term.Close(); err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: close: %w", w, i, err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
