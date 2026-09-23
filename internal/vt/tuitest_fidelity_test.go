package vt_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

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

// Leaving 47 or 1047 carries the alternate screen's cursor back to the primary
// one, because neither mode saves it. A program that sends that reset while the
// primary screen is already up, as many do defensively at startup or exit, has
// not left anything, so the cursor must stay where it is and not jump to
// wherever an alternate screen last had it.
func TestAltScreenResetOnPrimaryKeepsCursor(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"47", "1047"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			e := feed(t, 20, 6, "\x1b[?"+mode+"h\x1b[5;9Halt\x1b[?"+mode+"l"+
				"\x1b[2;3Hmain\x1b[?"+mode+"lX")
			if got, want := row(t, e, 1), "  mainX"; got != want {
				t.Errorf("primary row: got %q, want %q", got, want)
			}
		})
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

// A cell shift used to run through the buffer's Set, which blanks the other
// half of any wide rune it lands on. Inside a shift the cells being moved are
// still live, so blanking the neighbour of a cell that had just been copied
// erased the copy, and the blanking cascaded: one DCH on a line of CJK left the
// whole line empty. A test asserting that some text was gone would have passed
// against an emulator that had simply thrown the line away.
func TestDeleteCharKeepsWideRunes(t *testing.T) {
	t.Parallel()

	// Deleting both columns of the first rune leaves the rest flush left.
	e := feed(t, 10, 2, "\x1b[2J\x1b[H中日本\x1b[1;1H\x1b[2P")
	if got, want := row(t, e, 0), "日本"; got != want {
		t.Errorf("DCH 2: got %q, want %q", got, want)
	}

	// Deleting one column splits the first rune. Both halves go, and the rest
	// moves left by exactly the one column that was deleted, so the line still
	// occupies the width it should.
	e = feed(t, 10, 2, "\x1b[2J\x1b[H中日本\x1b[1;1H\x1b[1P")
	if got, want := row(t, e, 0), " 日本"; got != want {
		t.Errorf("DCH 1: got %q, want %q", got, want)
	}
}

// The mirror image: an insert used Set for the blanks it fills in, which
// reached back over the rune it had just shifted right and emptied it.
func TestInsertCharKeepsWideRunes(t *testing.T) {
	t.Parallel()

	e := feed(t, 12, 2, "\x1b[2J\x1b[H中日本\x1b[1;1H\x1b[1@")
	if got, want := row(t, e, 0), " 中日本"; got != want {
		t.Errorf("ICH 1: got %q, want %q", got, want)
	}

	e = feed(t, 12, 2, "\x1b[2J\x1b[H中日本\x1b[1;1H\x1b[2@")
	if got, want := row(t, e, 0), "  中日本"; got != want {
		t.Errorf("ICH 2: got %q, want %q", got, want)
	}
}

// A rune shifted so that its second half falls off the right margin leaves a
// lead with nothing to finish it, which no terminal can draw.
func TestShiftOffMarginBlanksOrphanedHalf(t *testing.T) {
	t.Parallel()

	e := feed(t, 6, 2, "\x1b[2J\x1b[Hab中日\x1b[1;1H\x1b[1@")
	if got, want := row(t, e, 0), " ab中"; got != want {
		t.Errorf("ICH pushing a wide rune off the margin: got %q, want %q", got, want)
	}
}

// The printable-ASCII fast path emitted its character immediately, so a
// combining mark arriving after it could not join it. The mark was written as a
// zero-width cell of its own at the cursor, which both lost the accent and
// blanked whatever stood in the next column.
func TestCombiningMarkJoinsAsciiBase(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[Héx")
	if got, want := row(t, e, 0), "éx"; got != want {
		t.Errorf("combining acute on ASCII: got %q, want %q", got, want)
	}
	if got, want := e.CursorPosition().X, 2; got != want {
		t.Errorf("cursor column: got %d, want %d", got, want)
	}
}

// The same defect seen from the other side: the mark took the cell to the right
// of its base, so text already on screen there disappeared.
func TestCombiningMarkLeavesNextColumnAlone(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[HAB\x1b[1;1Hé")
	if got, want := row(t, e, 0), "éB"; got != want {
		t.Errorf("combining mark over existing text: got %q, want %q", got, want)
	}
}

// The grapheme buffer is flushed at the end of every Write, so where a PTY read
// happened to fall decided whether a cluster was formed at all: the same bytes
// rendered as one cell or as two depending on chunking, which is a test that
// passes or fails by luck. Folding a continuation into the cell in front of it
// gives the same screen either way.
func TestGraphemeClusteringIsIndependentOfWriteChunking(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, whole string }{
		{"zwj", "\U0001F469‍\U0001F4BB!"},
		{"skin tone", "\U0001F44D\U0001F3FD!"},
		{"regional indicators", "\U0001F1EF\U0001F1F5!"},
		{"combining on wide", "中́!"},
		{"combining on ascii", "é!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := feed(t, 10, 2, "\x1b[2J\x1b[H"+tc.whole)
			wantRow, wantX := row(t, want, 0), want.CursorPosition().X

			// Split at every byte boundary the parser could see a read end on.
			for cut := 1; cut < len(tc.whole); cut++ {
				if !utf8.RuneStart(tc.whole[cut]) {
					continue
				}
				e := vt.NewEmulator(10, 2)
				if _, err := e.WriteString("\x1b[2J\x1b[H" + tc.whole[:cut]); err != nil {
					t.Fatalf("write: %v", err)
				}
				if _, err := e.WriteString(tc.whole[cut:]); err != nil {
					t.Fatalf("write: %v", err)
				}
				if got := row(t, e, 0); got != wantRow {
					t.Errorf("split at %d: row %q, want %q", cut, got, wantRow)
				}
				if got := e.CursorPosition().X; got != wantX {
					t.Errorf("split at %d: cursor %d, want %d", cut, got, wantX)
				}
			}
		})
	}
}

// Switching to the alternate screen used to home the cursor. None of 47, 1047
// or 1049 is defined to move it, so a program that positioned the cursor,
// flipped screens and wrote without repositioning landed in the wrong place,
// and on the way back out of 47 or 1047 it carried the wrong position with it.
func TestAltScreenKeepsCursorPosition(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"47", "1047", "1049"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			e := feed(t, 20, 6, "\x1b[2J\x1b[H\x1b[3;5Hmain\x1b[?"+mode+"hA")
			if got, want := row(t, e, 2), "        A"; got != want {
				t.Errorf("alt screen row: got %q, want %q", got, want)
			}
		})
	}
}

// SGR 21 is a double underline in ECMA-48 and in xterm. It was dropped, so
// text a program deliberately underlined came back unstyled.
func TestDoubleUnderline(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[H\x1b[21mD")
	if got := e.CellAt(0, 0).Style.Underline; got == ansi.UnderlineNone {
		t.Errorf("SGR 21: underline style %v, want an underline", got)
	}
}

// An underline subparameter the terminal does not recognise was left
// unconsumed, so "4:7" was read on as a bare SGR 7 and turned the cell reverse:
// a stray byte in a program's output silently inverted its colours.
//
// What the unknown style itself draws is a judgement call. This copy used to
// draw a single underline; tuios draws none and pins that in
// TestThemedSGR_UnderlineSubparamNoLeak. The copy follows tuios, so only the
// leak is asserted here.
func TestUnknownUnderlineSubparameterIsNotReverse(t *testing.T) {
	t.Parallel()

	e := feed(t, 10, 2, "\x1b[2J\x1b[H\x1b[4:7mD")
	if e.CellAt(0, 0).Style.Attrs&uv.AttrReverse != 0 {
		t.Error("SGR 4:7 set reverse video")
	}
}
