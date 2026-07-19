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
	// Line returns the plain text of a single physical row with trailing blanks
	// trimmed, or "" for an out-of-range row. It does not de-wrap: a logical
	// line that soft-wrapped at the right margin occupies several rows and will
	// not match as one string. Match per row, or use Text and account for the
	// wrap, or widen the terminal with WithSize so the line fits.
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

// Color is a cell color in one of three encodings. Only the fields matching
// Kind carry meaning; the others are zero.
type Color struct {
	// Kind selects which of the remaining fields is meaningful.
	Kind ColorKind
	// Index is the palette entry when Kind is ColorIndexed.
	Index uint8
	// R, G and B are the channel values when Kind is ColorRGB.
	R, G, B uint8
}

// Cell is a single grid cell with its rune and visual attributes.
type Cell struct {
	// Rune is the cell's first rune; combining marks are not exposed.
	Rune rune
	// Width is 1 for normal runes, 2 for wide runes, and 0 for the
	// continuation column that follows a wide rune.
	Width int
	// Fg and Bg are the foreground and background colors.
	Fg, Bg        Color
	Bold          bool
	Faint         bool
	Italic        bool
	Underline     bool
	Reverse       bool
	Strikethrough bool
	Blink         bool
	// Conceal reports SGR 8 (hidden). A real terminal draws a concealed cell as
	// a blank, so Line and Text render these cells as spaces; the rune is still
	// available here for a caller that needs to know what was concealed.
	Conceal bool
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
	out.Faint = st.Attrs&uv.AttrFaint != 0
	out.Conceal = st.Attrs&uv.AttrConceal != 0
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
		if c.Conceal {
			// SGR 8: a real terminal paints the cell blank, so reporting the
			// rune here would let a wait match text no user can see. One space
			// per non-continuation cell keeps a concealed line exactly as long
			// as the same line unconcealed.
			b.WriteByte(' ')
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
