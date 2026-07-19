package vt

import "testing"

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

			e := NewEmulator(80, 10)
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
// as given, so ordinary full-screen programs keep working.
func TestScrollRegionWithinScreenIsUnchanged(t *testing.T) {
	t.Parallel()

	e := NewEmulator(80, 24)
	if _, err := e.Write([]byte("\x1b[3;20r")); err != nil {
		t.Fatal(err)
	}

	got := e.scr.scroll
	if got.Min.Y != 2 || got.Max.Y != 20 {
		t.Fatalf("scroll region = [%d,%d), want [2,20)", got.Min.Y, got.Max.Y)
	}
}
