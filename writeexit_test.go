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
// Linux accepts writes to the master after the child has exited and discards
// the bytes, so there no write fails. A run ends as soon as a write succeeds
// after the exit was already reported, and the assertions have nothing to
// check on that platform.
//
// The loop is bounded so it cannot block or spin: it pauses between writes and
// sends fewer bytes than a PTY input buffer holds (4096 on Linux), so a write
// never waits on a child that is not reading.
//
// Verified to fail on macOS: removing the wait for the child in
// Terminal.inputErr makes the first assertion fire within a few runs.
func TestWriteErrorAfterExitReportsTheExit(t *testing.T) {
	t.Parallel()
	sh := shellPath(t)

	const (
		runs        = 50
		maxAttempts = 2000
		pause       = time.Millisecond
	)
	for i := range runs {
		term, err := tuitest.Start([]string{sh, "-c", "exit 0"}, tuitest.WithSize(20, 5))
		if err != nil {
			t.Fatalf("run %d: spawn: %v", i, err)
		}

		var werr error
		acceptedAfterExit := false
		for range maxAttempts {
			_, exitedBefore := term.ExitStatus()
			if werr = term.Type("x"); werr != nil {
				break
			}
			if exitedBefore {
				acceptedAfterExit = true
				break
			}
			time.Sleep(pause)
		}
		if werr == nil {
			_ = term.Close()
			if !acceptedAfterExit {
				t.Fatalf("run %d: the child neither exited nor refused input after %d writes", i, maxAttempts)
			}
			// The platform accepts writes to a finished program.
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
