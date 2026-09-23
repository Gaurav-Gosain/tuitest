package tuitest

import (
	"image/color"
	"strings"
	"sync"

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
	// Rune is the cell's first rune. A cell can hold a whole grapheme cluster,
	// so this is not always the character a user sees: "e" plus a combining
	// acute reports 'e' here. Use Content to compare against text.
	Rune rune
	// Content is the cell's full grapheme cluster: the base rune together with
	// any combining marks, joiners and modifiers that attach to it. It is what
	// Line and Text render, and what a caller should match against. Empty for
	// the continuation column of a wide rune.
	Content string
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

// text is what the cell puts on screen: the whole grapheme cluster, not just
// its first rune. A cell holding "e" plus a combining acute reads as "é" to a
// user, and reporting "e" would let WaitForText miss a string plainly on screen
// and let a golden record the accent as absent.
//
// Cell is an exported struct, so a caller (and several of this package's own
// tests) can build one with only Rune set. Falling back to Rune keeps those
// working rather than rendering them as nothing.
func (c Cell) text() string {
	if c.Content != "" {
		return c.Content
	}
	if c.Rune == 0 {
		return " "
	}
	return string(c.Rune)
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
		return Cell{Rune: ' ', Content: " ", Width: 1}
	}
	out := Cell{Width: c.Width, Rune: ' ', Content: c.Content}
	for _, r := range c.Content {
		out.Rune = r
		break
	}
	if c.Content == "" {
		out.Rune = ' '
		// A blank cell and the continuation column of a wide rune both arrive
		// with no content. Only the blank stands for a space on screen; the
		// continuation is skipped by Line, which keys off Width.
		if c.Width != 0 {
			out.Content = " "
		}
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
	// SGR 5 and SGR 6 are separate attributes but one visible effect, and a
	// caller asking "is this blinking" means either. Reporting only the slow
	// one let a golden record SGR 6 text as unstyled.
	out.Blink = st.Attrs&(uv.AttrBlink|uv.AttrRapidBlink) != 0
	out.Underline = st.Underline != uv.UnderlineStyleNone
	return out
}

// screenSnapshot is an immutable copy of the grid taken under the terminal lock.
//
// The terminal hands one snapshot to every reader until the grid changes, so
// the plain text of its rows is computed once, on first use, and shared.
type screenSnapshot struct {
	cols, rows int
	cells      [][]Cell // [row][col]
	curCol     int
	curRow     int
	curVisible bool
	exitCode   int
	exited     bool

	renderOnce sync.Once
	lines      []string // plain text per row, see renderLine
	text       string   // the rows joined, see Text
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

// resized returns a copy of s cut or padded with blank cells to cols by rows,
// with the cursor kept inside it. A wide rune whose second column falls off the
// right edge is blanked, since half of it cannot be shown.
func (s *screenSnapshot) resized(cols, rows int) *screenSnapshot {
	blank := Cell{Rune: ' ', Content: " ", Width: 1}
	cells := make([][]Cell, rows)
	for row := range cells {
		line := make([]Cell, cols)
		for col := range line {
			line[col] = blank
		}
		if row < len(s.cells) {
			copy(line, s.cells[row])
			if last := len(s.cells[row]); last > cols && cols > 0 && line[cols-1].Width == 2 {
				line[cols-1] = blank
			}
		}
		cells[row] = line
	}
	return &screenSnapshot{
		cols:       cols,
		rows:       rows,
		cells:      cells,
		curCol:     max(0, min(s.curCol, cols-1)),
		curRow:     max(0, min(s.curRow, rows-1)),
		curVisible: s.curVisible,
		exitCode:   s.exitCode,
		exited:     s.exited,
	}
}

// render fills in the per-row text and the joined text, once.
func (s *screenSnapshot) render() {
	s.renderOnce.Do(func() {
		s.lines = make([]string, len(s.cells))
		for row := range s.cells {
			s.lines[row] = renderLine(s.cells[row])
		}
		// Drop trailing blank lines. rows disagrees with len(cells) only in a
		// snapshot built by hand, and then the smaller one wins.
		end := min(s.rows, len(s.lines))
		for end > 0 && s.lines[end-1] == "" {
			end--
		}
		s.text = strings.Join(s.lines[:end], "\n")
	})
}

func (s *screenSnapshot) Line(row int) string {
	if row < 0 || row >= len(s.cells) {
		return ""
	}
	s.render()
	return s.lines[row]
}

func (s *screenSnapshot) Text() string {
	s.render()
	return s.text
}

// renderLine is the plain text of one row with trailing blanks trimmed.
func renderLine(cells []Cell) string {
	var b strings.Builder
	for _, c := range cells {
		if c.Width == 0 {
			// Continuation column of a wide rune; already emitted.
			continue
		}
		if c.Conceal {
			// SGR 8: a real terminal paints the cell blank, so reporting the
			// rune here would let a wait match text no user can see. One space
			// per column the cell covers keeps a concealed line exactly as long
			// as the same line unconcealed.
			for i := 0; i < c.Width; i++ {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteString(c.text())
	}
	return strings.TrimRight(b.String(), " ")
}
