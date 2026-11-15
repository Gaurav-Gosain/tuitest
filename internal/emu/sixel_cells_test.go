package emu

import "testing"

// A sixel image leaves no characters on the screen: the cells it covers read
// as blanks, where tuios.dev/vt keeps marker codepoints of its own.
func TestSixelCellsReadAsBlanks(t *testing.T) {
	e := New(20, 5)
	// A 2 by 1 cell image: 16 pixels wide, 6 high, at the fallback 10x20 cell.
	_, _ = e.Write([]byte("ab\x1bPq#0;2;100;0;0#0~~~~~~~~~~~~~~~~\x1b\\"))
	for col := range 20 {
		for row := range 5 {
			c := e.CellAt(col, row)
			if c == nil {
				continue
			}
			for _, r := range c.Content {
				if r >= 0xf0000 {
					t.Fatalf("cell %d,%d holds the private codepoint %U, want a blank", col, row, r)
				}
			}
		}
	}
	if got := e.CellAt(0, 0).Content; got != "a" {
		t.Fatalf("cell 0,0 = %q, want the text before the image", got)
	}
}
