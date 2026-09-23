package vt

import (
	"strings"
	"testing"
	"time"
)

// TestEraseCharacterIsBoundedByTheLine pins the cost of ECH to the screen
// rather than to the number the program sent.
//
// The count arrives straight off the wire and a program is free to send one far
// larger than the screen. Nothing reads the PTY while a write is being
// processed, so an erase that walks a billion cells does not merely waste time:
// the program under test fills the pipe and blocks, and the run stalls
// somewhere with no visible connection to the erase that caused it. The
// grammar fuzzer found this as a single sequence costing six seconds, which cut
// its own throughput by two orders of magnitude.
//
// The oversized case is spelled with more digits than an int holds because that
// is what actually reaches a handler: the parameter parser rejects a value with
// every bit set and hands back the default, while a longer run of digits wraps
// to whatever it wraps to, which is how a count of a billion and a half gets in.
func TestEraseCharacterIsBoundedByTheLine(t *testing.T) {
	t.Parallel()

	for _, count := range []string{"70", "999999", "99999999999999999999"} {
		t.Run(count, func(t *testing.T) {
			t.Parallel()

			e := NewEmulator(80, 24)
			if _, err := e.WriteString("\x1b[1;1H" + strings.Repeat("x", 80) + "\x1b[1;11H"); err != nil {
				t.Fatalf("setting up the line: %v", err)
			}

			// Erasing from column 11 clears the 70 cells to the end of the
			// line whatever the count says, and takes no longer than that.
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = e.WriteString("\x1b[" + count + "X")
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("ECH with a count of %s took over a second, so its cost follows the parameter rather than the screen", count)
			}

			line := strings.TrimRight(strings.SplitN(e.String(), "\n", 2)[0], " ")
			if want := strings.Repeat("x", 10); line != want {
				t.Errorf("after ECH the line is %q, want %q", line, want)
			}
		})
	}
}
