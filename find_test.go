package tuitest

import "testing"

// TestFindReturnsCellColumns checks Find against the two ways a text offset
// and a cell column part: bytes wider than one (a box-drawing border is three
// bytes in one cell) and cells wider than one (a CJK character is two cells).
func TestFindReturnsCellColumns(t *testing.T) {
	const (
		border = "\xe2\x94\x82" // U+2502, three bytes, one cell
		cjk    = "\xe4\xb8\x96" // U+4E16, three bytes, two cells
		cjk2   = "\xe7\x95\x8c" // U+754C
		acute  = "\xcc\x81"     // U+0301, a combining mark
	)
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("top\r\n" + border + " " + cjk + cjk2 + " target\r\ne" + acute + " x"))
	s := term.Screen()

	cases := []struct {
		substr   string
		col, row int
		ok       bool
	}{
		{"top", 0, 0, true},
		// The border is column 0, a space column 1, the two CJK characters
		// columns 2-3 and 4-5, and another space column 6.
		{"target", 7, 1, true},
		{cjk2, 4, 1, true},
		// "e" plus a combining acute is one cell holding two runes.
		{"x", 2, 2, true},
		{acute, 0, 2, true},
		{"absent", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range cases {
		col, row, ok := Find(s, tc.substr)
		if col != tc.col || row != tc.row || ok != tc.ok {
			t.Errorf("Find(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.substr, col, row, ok, tc.col, tc.row, tc.ok)
		}
	}
}
