package tuitest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// A write that fails because the program has just exited must say so, and the
// terminal must already report the exit when the error comes back.
//
// On macOS a write to the PTY master fails with EIO as soon as the child has
// closed its end, which happens before the output pump has drained the master
// and reaped the child. A caller that saw the error and then asked whether the
// child had exited was told no, and concluded the harness itself had broken.
// The fuzzer did exactly that and reported "driving the program failed: write
// /dev/ptmx: input/output error" as a crash of a program that had simply quit.
//
// Linux accepts writes to a master whose slave has closed, so there the loop
// below only ends at the deadline and the assertions have nothing to check.
//
// Verified to fail on macOS: removing the wait for the child in
// Terminal.writeErr makes the first assertion fire within a few runs.
func TestWriteErrorAfterExitReportsTheExit(t *testing.T) {
	t.Parallel()
	sh := shellPath(t)

	const runs = 50
	for i := range runs {
		term, err := tuitest.Start([]string{sh, "-c", "exit 0"}, tuitest.WithSize(20, 5))
		if err != nil {
			t.Fatalf("run %d: spawn: %v", i, err)
		}

		var werr error
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if werr = term.Type("x"); werr != nil {
				break
			}
		}
		if werr == nil {
			// The platform accepts writes to a finished program.
			_ = term.Close()
			continue
		}
		if _, exited := term.ExitStatus(); !exited {
			_ = term.Close()
			t.Fatalf("run %d: the write failed with %v but the child is not reported as exited", i, werr)
		}
		if !errors.Is(werr, tuitest.ErrChildExited) {
			t.Errorf("run %d: the write error %v does not wrap ErrChildExited", i, werr)
		}
		if err := term.Close(); err != nil {
			t.Fatalf("run %d: close: %v", i, err)
		}
	}
}
