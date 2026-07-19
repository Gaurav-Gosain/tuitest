package vt_test

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/vt"
)

// Regression tests for divergences found by differential testing against
// ghostty-vt (see ../../vtdiff_test.go). Every expectation here is the answer
// ghostty produced for the same byte stream, cross-checked against tmux where
// the two references disagreed. They are ordinary unit tests so the fixes stay
// enforced on a machine with neither node nor the reference wasm.

// row renders one screen row as plain text with trailing blanks trimmed.
func row(t *testing.T, e *vt.Emulator, y int) string {
	t.Helper()
	var b strings.Builder
	for x := 0; x < e.Width(); x++ {
		c := e.CellAt(x, y)
		if c == nil {
			b.WriteByte(' ')
			continue
		}
		if c.Width == 0 {
			// Continuation column of a wide rune; already emitted.
			continue
		}
		if c.Content == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Content)
	}
	return strings.TrimRight(b.String(), " ")
}

func feed(t *testing.T, cols, rows int, s string) *vt.Emulator {
	t.Helper()
	e := vt.NewEmulator(cols, rows)
	if _, err := e.WriteString(s); err != nil {
		t.Fatalf("write: %v", err)
	}
	return e
}

// SO and SI are C0 controls, but their handlers were registered in the loop
// over C1 controls, so they were never installed at all: a program that
// designates the line-drawing set into G1 and shifts to it printed raw ASCII.
func TestShiftOutSelectsG1(t *testing.T) {
	t.Parallel()

	e := feed(t, 20, 2, "\x1b)0\x0elqk\x0f|lqk")
	if got, want := row(t, e, 0), "┌─┐|lqk"; got != want {
		t.Errorf("SO/SI line drawing: got %q, want %q", got, want)
	}
}

// A wide rune must not straddle the right margin. It used to be written at the
// last column anyway, where the buffer refused it and blanked the wide rune to
// its left, so a CJK line silently lost its last two characters.
func TestWideRuneWrapsAtRightMargin(t *testing.T) {
	t.Parallel()

	// Ten columns hold exactly five wide runes; the sixth and seventh belong
	// on the next row.
	e := feed(t, 10, 3, "\x1b[2J\x1b[H"+strings.Repeat("中", 7))
	if got, want := row(t, e, 0), "中中中中中"; got != want {
		t.Errorf("row 0: got %q, want %q", got, want)
	}
	if got, want := row(t, e, 1), "中中"; got != want {
		t.Errorf("row 1: got %q, want %q", got, want)
	}
	x, y := e.CursorPosition().X, e.CursorPosition().Y
	if x != 4 || y != 1 {
		t.Errorf("cursor: got (%d,%d), want (4,1)", x, y)
	}
}

// Insert mode shifts the rest of the line right instead of overwriting it.
// IRM was accepted as a mode but never consulted when printing.
func TestInsertReplaceMode(t *testing.T) {
	t.Parallel()

	e := feed(t, 20, 2, "\x1b[2J\x1b[Habcdefgh\x1b[1;3H\x1b[4hXY\x1b[4l")
	if got, want := row(t, e, 0), "abXYcdefgh"; got != want {
		t.Errorf("insert mode: got %q, want %q", got, want)
	}
}

// ED 1 erases from the start of the screen up to and including the cursor. It
// used to erase the whole cursor row, wiping text the program still expected
// to be on screen.
func TestEraseDisplayAbovePreservesRestOfCursorRow(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 3, "\x1b[2J\x1b[Haaaaaaaaaa\r\nbbbbbbbbbb\x1b[2;5H\x1b[1J")
	if got, want := row(t, e, 0), ""; got != want {
		t.Errorf("row 0: got %q, want %q", got, want)
	}
	if got, want := row(t, e, 1), "     bbbbb"; got != want {
		t.Errorf("row 1: got %q, want %q", got, want)
	}
}

// ED 3 is xterm's "erase saved lines": it drops scrollback and leaves the
// visible screen alone. It used to clear the display as well, so a program
// that trimmed its history lost the frame it was drawing.
func TestEraseSavedLinesKeepsScreen(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[Hkeep me\x1b[3J")
	if got, want := row(t, e, 0), "keep me"; got != want {
		t.Errorf("row 0 after ED 3: got %q, want %q", got, want)
	}
}

// Mode 47 is the original alternate screen and is still smcup for older
// terminfo entries. It was unhandled, so the full-screen UI was drawn over the
// primary screen and the primary contents never came back.
func TestAltScreen47RestoresPrimary(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[Hprimary\x1b[?47h\x1b[2J\x1b[Halt\x1b[?47l")
	if got, want := row(t, e, 0), "primary"; got != want {
		t.Errorf("primary screen: got %q, want %q", got, want)
	}
}

// CSI s saved the cursor but CSI u was not registered, so every save/restore
// pair left the cursor wherever the program had last drawn.
func TestSaveRestoreCursorSCO(t *testing.T) {
	t.Parallel()

	e := feed(t, 20, 4, "\x1b[2J\x1b[2;3H\x1b[s\x1b[4;10HX\x1b[uY")
	if got, want := row(t, e, 1), "  Y"; got != want {
		t.Errorf("restored row: got %q, want %q", got, want)
	}
}

// DECALN fills the screen with E and homes the cursor. vttest opens with it,
// and an emulator that ignores it reports a blank screen for every alignment
// test.
func TestScreenAlignmentPattern(t *testing.T) {
	t.Parallel()

	e := feed(t, 5, 2, "\x1b#8")
	for y := range 2 {
		if got, want := row(t, e, y), "EEEEE"; got != want {
			t.Errorf("row %d: got %q, want %q", y, got, want)
		}
	}
}

// ESC E (NEL) and ESC N (SS2) had no handlers: NEL was ignored entirely, and
// the single shift only worked when sent as the 8-bit C1 control, which no
// UTF-8 program emits.
func TestNextLineAndSingleShift(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 3, "\x1b[2J\x1b[Habc\x1bEdef")
	if got, want := row(t, e, 1), "def"; got != want {
		t.Errorf("NEL: got %q, want %q", got, want)
	}

	e = feed(t, 10, 2, "\x1b[2J\x1b[H\x1b*0\x1bNlx")
	if got, want := row(t, e, 0), "┌x"; got != want {
		t.Errorf("SS2: got %q, want %q", got, want)
	}
}

// DECSED and DECSEL, the "CSI ? Ps J" and "CSI ? Ps K" forms, were not
// registered at all, which made them silent no-ops instead of erasing.
func TestSelectiveEraseErases(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[Habcdefghij\x1b[1;4H\x1b[?0K")
	if got, want := row(t, e, 0), "abc"; got != want {
		t.Errorf("DECSEL: got %q, want %q", got, want)
	}

	e = feed(t, 10, 2, "\x1b[2J\x1b[Habcdefghij\x1b[1;4H\x1b[?1J")
	if got, want := row(t, e, 0), "    efghij"; got != want {
		t.Errorf("DECSED: got %q, want %q", got, want)
	}
}

// HPB and VPB move the cursor back by columns and rows. Both were missing, so
// the sequences were ignored and everything after them landed in the wrong
// place.
func TestPositionBackward(t *testing.T) {
	t.Parallel()

	e := feed(t, 20, 6, "\x1b[2J\x1b[1;1H\x1b[10a\x1b[5eX\x1b[3jY\x1b[2kZ")
	if got, want := row(t, e, 5), "        Y X"; got != want {
		t.Errorf("HPB row: got %q, want %q", got, want)
	}
	if got, want := row(t, e, 3), "         Z"; got != want {
		t.Errorf("VPB row: got %q, want %q", got, want)
	}
}
