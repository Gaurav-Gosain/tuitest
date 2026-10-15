package emu_test

import (
	"strconv"
	"strings"
	"testing"

	"tuios.dev/vt"
)

// A program's idea of the terminal size lags the real one: after a resize it
// keeps drawing at the old dimensions until it processes SIGWINCH. So a
// DECSTBM whose bottom margin is past the bottom of the screen is not
// malformed input, it is the ordinary consequence of a resize, and the
// emulator has to survive it.
//
// Before the margins were clamped, the region was stored verbatim and the next
// scrolling operation indexed the line buffer past its end and panicked, which
// took down the whole harness rather than just producing a wrong screen. It was
// found by pointing tuitest fuzz at nano.
func TestScrollRegionBeyondScreenDoesNotPanic(t *testing.T) {
	t.Parallel()

	ops := []struct {
		name string
		seq  string
	}{
		// Reverse index at the top of the region scrolls the region down.
		{"reverse index", "\x1bM"},
		// Insert and delete line operate on the region directly.
		{"insert line", "\x1b[L"},
		{"delete line", "\x1b[M"},
		// Explicit scroll up and down.
		{"scroll up", "\x1b[S"},
		{"scroll down", "\x1b[T"},
		// A plain line feed at the bottom margin scrolls too.
		{"line feed", "\n\n\n\n\n\n\n\n\n\n\n\n"},
	}

	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()

			e := vt.NewEmulator(80, 10)
			// A margin bottom of 24 on a 10-row screen, exactly what a program
			// that has not yet seen the resize sends.
			if _, err := e.Write([]byte("\x1b[1;24r")); err != nil {
				t.Fatalf("setting the scroll region: %v", err)
			}
			if _, err := e.Write([]byte("\x1b[1;1H")); err != nil {
				t.Fatalf("homing the cursor: %v", err)
			}

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked with a scroll region past the bottom of the screen: %v", op.name, r)
				}
			}()

			for i := 0; i < 40; i++ {
				if _, err := e.Write([]byte(op.seq)); err != nil {
					t.Fatalf("writing %q: %v", op.seq, err)
				}
			}
		})
	}
}

// The clamp must not break a legitimate scroll region: one that fits is stored
// as given, so ordinary full-screen programs keep working. A line feed at the
// bottom margin scrolls rows 3 to 20 and leaves the rows outside alone.
func TestScrollRegionWithinScreenIsUnchanged(t *testing.T) {
	t.Parallel()

	e := vt.NewEmulator(80, 24)
	var fill strings.Builder
	for row := 1; row <= 24; row++ {
		fill.WriteString("\x1b[" + strconv.Itoa(row) + ";1H" + strconv.Itoa(row))
	}
	if _, err := e.WriteString(fill.String() + "\x1b[3;20r\x1b[20;1H\n"); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(e.String(), "\n")
	for row, want := range map[int]string{1: "1", 2: "2", 3: "4", 19: "20", 20: "", 21: "21", 24: "24"} {
		if got := strings.TrimRight(lines[row-1], " "); got != want {
			t.Errorf("row %d = %q, want %q", row, got, want)
		}
	}
}
