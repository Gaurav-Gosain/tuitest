package tuitest

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Screen is a read-only view of the terminal grid handed to wait conditions and
// returned by snapshots. Every Screen value is an immutable copy taken under the
// terminal's lock, so a condition callback may stash it without observing a torn
// write from the output pump.
type Screen interface {
	// Size returns the grid size in cells.
	Size() (cols, rows int)
	// Cell returns the cell at the given zero-based column and row. Out-of-bounds
	// coordinates return the zero Cell.
	Cell(col, row int) Cell
	// Cursor returns the cursor position (zero-based) and whether it is visible.
	Cursor() (col, row int, visible bool)
	// Text returns the plain-text screen, one row per line, with each line's
	// trailing blanks trimmed and trailing blank lines dropped.
	Text() string
	// Line returns the plain text of a single row with trailing blanks trimmed.
	Line(row int) string
	// ExitCode reports the child's exit code and whether it has exited.
	ExitCode() (code int, exited bool)
}

// ColorKind distinguishes the three color encodings a cell can carry.
type ColorKind int

const (
	// ColorDefault is the terminal's default foreground or background.
	ColorDefault ColorKind = iota
	// ColorIndexed is a palette color 0-255.
	ColorIndexed
	// ColorRGB is a 24-bit true color.
	ColorRGB
)

// Color is a cell color in one of three encodings.
type Color struct {
	Kind    ColorKind
	Index   uint8 // when Kind == ColorIndexed
	R, G, B uint8 // when Kind == ColorRGB
}

// Cell is a single grid cell with its rune and visual attributes.
type Cell struct {
	Rune          rune
	Width         int // 1 for normal runes, 2 for wide runes, 0 for a wide-rune continuation column
	Fg, Bg        Color
	Bold          bool
	Italic        bool
	Underline     bool
	Reverse       bool
	Strikethrough bool
	Blink         bool
}

func toColor(c color.Color) Color {
	if c == nil {
		return Color{Kind: ColorDefault}
	}
	switch v := c.(type) {
	case ansi.BasicColor:
		return Color{Kind: ColorIndexed, Index: uint8(v)}
	case ansi.IndexedColor:
		return Color{Kind: ColorIndexed, Index: uint8(v)}
	default:
		r, g, b, _ := c.RGBA()
		return Color{Kind: ColorRGB, R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
}

func toCell(c *uv.Cell) Cell {
	if c == nil {
		return Cell{Rune: ' ', Width: 1}
	}
	out := Cell{Width: c.Width, Rune: ' '}
	for _, r := range c.Content {
		out.Rune = r
		break
	}
	if c.Content == "" {
		out.Rune = ' '
	}
	st := c.Style
	out.Fg = toColor(st.Fg)
	out.Bg = toColor(st.Bg)
	out.Bold = st.Attrs&uv.AttrBold != 0
	out.Italic = st.Attrs&uv.AttrItalic != 0
	out.Reverse = st.Attrs&uv.AttrReverse != 0
	out.Strikethrough = st.Attrs&uv.AttrStrikethrough != 0
	out.Blink = st.Attrs&uv.AttrBlink != 0
	out.Underline = st.Underline != uv.UnderlineStyleNone
	return out
}

// screenSnapshot is an immutable copy of the grid taken under the terminal lock.
type screenSnapshot struct {
	cols, rows int
	cells      [][]Cell // [row][col]
	curCol     int
	curRow     int
	curVisible bool
	exitCode   int
	exited     bool
}

func (s *screenSnapshot) Size() (int, int) { return s.cols, s.rows }

func (s *screenSnapshot) Cell(col, row int) Cell {
	if row < 0 || row >= len(s.cells) || col < 0 || col >= len(s.cells[row]) {
		return Cell{}
	}
	return s.cells[row][col]
}

func (s *screenSnapshot) Cursor() (int, int, bool) { return s.curCol, s.curRow, s.curVisible }

func (s *screenSnapshot) ExitCode() (int, bool) { return s.exitCode, s.exited }

func (s *screenSnapshot) Line(row int) string {
	if row < 0 || row >= len(s.cells) {
		return ""
	}
	var b strings.Builder
	for col := 0; col < len(s.cells[row]); col++ {
		c := s.cells[row][col]
		if c.Width == 0 {
			// Continuation column of a wide rune; already emitted.
			continue
		}
		b.WriteRune(c.Rune)
	}
	return strings.TrimRight(b.String(), " ")
}

func (s *screenSnapshot) Text() string {
	lines := make([]string, s.rows)
	for row := 0; row < s.rows; row++ {
		lines[row] = s.Line(row)
	}
	// Drop trailing blank lines.
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}
