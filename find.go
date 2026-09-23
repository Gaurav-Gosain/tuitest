package tuitest

import "strings"

// Find locates the first occurrence of substr on the screen, scanning rows top
// to bottom, and returns the zero-based cell column and row where it starts.
// It reports false when no single row contains substr, which includes text
// that soft-wrapped across two rows.
//
// The column is a cell column, the coordinate SendMouse takes, and not an
// offset into the row's text. The two differ as soon as anything before the
// match is not one byte wide: a box-drawing border is three bytes and one cell,
// a CJK character or an emoji is two cells, so clicking at a byte or rune
// offset lands to one side of the text.
func Find(s Screen, substr string) (col, row int, ok bool) {
	if substr == "" {
		return 0, 0, false
	}
	cols, rows := s.Size()
	for row := 0; row < rows; row++ {
		// Rebuild the row's text as Line renders it, remembering the column
		// each byte of it came from.
		var b strings.Builder
		var colOf []int
		for c := 0; c < cols; c++ {
			cell := s.Cell(c, row)
			if cell.Width == 0 {
				continue
			}
			text := cell.text()
			if cell.Conceal {
				text = strings.Repeat(" ", cell.Width)
			}
			for range len(text) {
				colOf = append(colOf, c)
			}
			b.WriteString(text)
		}
		if i := strings.Index(b.String(), substr); i >= 0 {
			return colOf[i], row, true
		}
	}
	return 0, 0, false
}
