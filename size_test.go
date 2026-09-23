package tuitest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// A size the PTY cannot carry has to be refused, not half applied. The kernel
// stores the window size in 16-bit fields, and the emulator and the PTY used to
// take the requested size each in their own way: WithSize(0, 0) gave a 0x0
// screen over an 80x24 PTY, 70000 columns became 4464 in the PTY and 70000 in
// the emulator, and a negative size panicked inside the emulator, in Resize
// with the terminal's lock held.
//
// Verified to fail: removing the checkSize calls from Start and Resize makes
// the negative cases panic and the others report a screen the program does
// not see.
func TestSizesThePTYCannotCarryAreRefused(t *testing.T) {
	t.Parallel()
	sh := shellPath(t)

	bad := [][2]int{{0, 0}, {0, 24}, {80, 0}, {-3, -3}, {-1, 5}, {70000, 3}, {80, 65536}}

	for _, sz := range bad {
		term, err := startNoPanic(sh, tuitest.WithSize(sz[0], sz[1]))
		if err == nil {
			_ = term.Close()
			t.Errorf("Start with WithSize(%d, %d) succeeded", sz[0], sz[1])
		} else if !strings.Contains(err.Error(), "size") {
			t.Errorf("Start with WithSize(%d, %d): error %q does not mention the size", sz[0], sz[1], err)
		}
	}

	term := tuitest.StartT(t, []string{sh, "-c", "sleep 30"}, tuitest.WithSize(20, 5))
	for _, sz := range bad {
		if err := resizeNoPanic(t, term, sz[0], sz[1]); err == nil {
			t.Errorf("Resize(%d, %d) succeeded", sz[0], sz[1])
		}
		if c, r := term.Screen().Size(); c != 20 || r != 5 {
			t.Fatalf("a refused Resize(%d, %d) changed the screen to %dx%d", sz[0], sz[1], c, r)
		}
	}

	// The limits themselves are accepted and the PTY agrees with the screen.
	if err := term.Resize(1, 1); err != nil {
		t.Errorf("Resize(1, 1): %v", err)
	}
}

// The size the program sees is the size the screen reports.
func TestPTYAndScreenAgreeOnTheSize(t *testing.T) {
	t.Parallel()
	sh := shellPath(t)

	term := tuitest.StartT(t, []string{sh, "-c", "stty size; read x; stty size; sleep 30"}, tuitest.WithSize(33, 7))
	if err := term.WaitForText("7 33", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(41, 9); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys(tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("9 41", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if c, r := term.Screen().Size(); c != 41 || r != 9 {
		t.Fatalf("screen is %dx%d, want 41x9", c, r)
	}
}

// startNoPanic starts a long-running shell and turns a panic in Start into an
// error, so one bad size does not end the whole table.
func startNoPanic(sh string, opts ...tuitest.Option) (term *tuitest.Terminal, err error) {
	defer func() {
		if r := recover(); r != nil {
			term, err = nil, fmt.Errorf("Start panicked: %v", r)
		}
	}()
	return tuitest.Start([]string{sh, "-c", "sleep 30"}, opts...)
}

// resizeNoPanic fails the test at once if Resize panics. The panic leaves the
// terminal's lock held, so carrying on would only hang the next call.
func resizeNoPanic(t *testing.T, term *tuitest.Terminal, cols, rows int) error {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Resize(%d, %d) panicked: %v", cols, rows, r)
		}
	}()
	return term.Resize(cols, rows)
}
