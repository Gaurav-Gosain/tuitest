package vt

import (
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// kittyPlaceholderChar is the base character used by kitty's unicode
// placeholder image protocol (U=1). Apps like yazi emit this character
// with combining diacritical marks to encode image-id/row/column.
// tuios handles kitty graphics via a separate overlay layer, so these
// placeholder characters should be invisible in the text buffer.
const kittyPlaceholderChar = 0x10EEEE

// asciiStr holds the 128 single-byte ASCII strings so the printable-ASCII fast
// path in handlePrint can pass a package-lifetime string to handleGrapheme
// instead of allocating string(r) (which escapes to the heap) for every char.
var asciiStr [128]string

func init() {
	for i := range asciiStr {
		asciiStr[i] = string(rune(i))
	}
}

// handlePrint handles printable characters.
func (e *Emulator) handlePrint(r rune) {
	// Suppress kitty unicode placeholder characters. They would show as
	// garbled text because tuios renders images via its own passthrough
	// layer, not by interpreting placeholder cells.
	if r == kittyPlaceholderChar {
		return
	}
	if r >= ansi.SP && r < ansi.DEL {
		if len(e.grapheme) > 0 {
			// If we have a grapheme buffer, flush it before handling the ASCII character.
			e.flushGrapheme()
		}
		e.handleGrapheme(asciiStr[r], 1)
	} else {
		e.grapheme = append(e.grapheme, r)
	}
}

// flushGrapheme flushes the current grapheme buffer, if any, and handles the
// grapheme as a single unit.
func (e *Emulator) flushGrapheme() {
	if len(e.grapheme) == 0 {
		return
	}

	// We always use ansi.GraphemeWidth here to report accurate widths
	// and it's up to the caller to decide how to handle Unicode vs non-Unicode
	// modes.
	method := ansi.GraphemeWidth
	graphemes := string(e.grapheme)
	for len(graphemes) > 0 {
		cluster, width := ansi.FirstGraphemeCluster(graphemes, method)
		e.handleGrapheme(cluster, width)
		graphemes = graphemes[len(cluster):]
	}
	e.grapheme = e.grapheme[:0] // Reset the grapheme buffer.
}

// printedCell remembers the cell the last grapheme cluster was written to, so
// that a cluster arriving afterwards can be tested for whether it continues
// that one. It carries enough to tell "still the cell I wrote" from "something
// has happened since" without every cursor move and erase having to invalidate
// it by hand: the cursor must not have moved off where printing left it, and
// the cell must still hold what was put there.
type printedCell struct {
	scr        *Screen
	x, y       int
	curX, curY int
	content    string
	valid      bool
}

// extendPrinted folds content into the cell printed immediately before, when
// Unicode says the two are one grapheme, and reports whether it did.
//
// This is what a terminal does with a combining mark: the base rune is drawn
// as soon as it arrives, and the mark modifies the cell already on screen. The
// alternative, holding a cluster back until the next character proves it
// complete, cannot work in a harness, because the last character of a burst
// would never appear.
//
// It also removes a source of flaky results. The grapheme buffer is flushed at
// the end of every Write, so where a PTY read happens to split "a" from its
// accent, or an emoji from its zero-width joiner, decided whether they were
// clustered at all. Folding a continuation into the cell in front of it gives
// the same screen either way.
//
// That includes the cluster's width. A presentation selector or a keycap mark
// turns a narrow base wide, so the cell is given the width of the whole cluster
// and the cursor is moved to match, exactly as if the cluster had arrived in
// one piece. Nothing has been laid out against the old width yet: the checks
// below refuse to extend once the cursor has moved or the cell has changed.
// Keeping the base's width instead drew "❤️" one column wide whenever a PTY
// read ended between U+2764 and U+FE0F, and every character after it on the
// row one column early. tuios's emulator widens the cell the same way.
func (e *Emulator) extendPrinted(content string) bool {
	p := &e.printed
	if !p.valid || p.scr != e.scr {
		return false
	}
	curX, curY := e.scr.CursorPosition()
	if curX != p.curX || curY != p.curY {
		return false
	}
	prev := e.scr.CellAt(p.x, p.y)
	if prev == nil || prev.Content != p.content {
		return false
	}

	joined := prev.Content + content
	cluster, width := ansi.FirstGraphemeCluster(joined, ansi.GraphemeWidth)
	if len(cluster) != len(joined) {
		return false
	}

	cell := *prev
	cell.Content = joined
	if width < 1 || width == cell.Width {
		e.scr.SetCell(p.x, p.y, &cell)
		p.content = joined
		return true
	}

	awm := e.isModeSet(ansi.ModeAutoWrap)
	scrWidth := e.scr.Width()
	if p.x+width > scrWidth {
		if !awm {
			// The unsplit cluster would not fit either, and with autowrap
			// off there is no next line to put it on. The base keeps its
			// cell and the continuation is dropped, so the grid never holds
			// a cell narrower than its content measures.
			return true
		}
		// The widened cluster no longer fits where its base was drawn. Take
		// the base back and draw the whole cluster from that position, which
		// wraps it to the next line the way the unsplit write does.
		e.scr.SetCell(p.x, p.y, nil)
		e.scr.setCursor(p.x, p.y, false)
		e.atPhantom = false
		p.valid = false
		e.handleGrapheme(joined, width)
		return true
	}

	cell.Width = width
	e.scr.SetCell(p.x, p.y, &cell)
	// The same cursor rule handleGrapheme applies after drawing a cell.
	x := p.x
	e.atPhantom = awm && x+width >= scrWidth
	if !e.atPhantom {
		x += width
	}
	e.scr.setCursor(x, p.y, false)
	p.curX, p.curY = e.scr.CursorPosition()
	p.content = joined
	return true
}

// handleGrapheme handles UTF-8 graphemes.
func (e *Emulator) handleGrapheme(content string, width int) {
	// Only a cluster starting outside ASCII can continue the one before it, so
	// the check costs the common path nothing.
	if len(content) > 0 && content[0] >= utf8.RuneSelf && e.extendPrinted(content) {
		return
	}
	e.printed.valid = false

	if width < 1 {
		// A cluster with no width of its own and nothing in front of it to
		// attach to: a combining mark opening a line, or one whose base has
		// since been overwritten. Giving it a cell would be worse than dropping
		// it, since the cell would take a column away from whatever stood there
		// and then not be drawn. Dropped here, before anything below can move
		// the cursor, so a stray mark cannot wrap a line on its own. A cluster
		// the charset tables rewrite is a single byte and always one column
		// wide, so nothing below can widen this.
		return
	}

	awm := e.isModeSet(ansi.ModeAutoWrap)
	cell := uv.Cell{
		Content: content,
		Width:   width,
		Style:   e.scr.cursorPen(),
		Link:    e.scr.cursorLink(),
	}

	x, y := e.scr.CursorPosition()
	if e.atPhantom && awm {
		// moves cursor down similar to [Terminal.linefeed] except it doesn't
		// respects [ansi.LNM] mode.
		// This will reset the phantom state i.e. pending wrap state.
		e.index()
		_, y = e.scr.CursorPosition()
		x = 0
	}

	// Handle character set mappings
	if len(content) == 1 { //nolint:nestif
		var charset CharSet
		c := content[0]
		if e.gsingle > 1 && e.gsingle < 4 {
			charset = e.charsets[e.gsingle]
			e.gsingle = 0
		} else if c < 128 {
			charset = e.charsets[e.gl]
		} else {
			charset = e.charsets[e.gr]
		}

		if charset != nil {
			if r, ok := charset[c]; ok {
				cell.Content = r
				cell.Width = 1
			}
		}
	}

	if cell.Width == 1 && len(content) == 1 {
		e.lastChar, _ = utf8.DecodeRuneInString(content)
	}

	// A wide cell may not straddle the right margin. Without this the wide
	// rune is written at the last column, where the buffer both refuses to
	// place it and blanks the wide rune to its left, so a CJK or emoji line
	// silently loses its last two characters.
	scrWidth := e.scr.Width()
	if x+cell.Width > scrWidth {
		if !awm {
			// Autowrap off: there is nowhere for the character to go, and the
			// cursor stays pinned at the margin.
			return
		}
		e.index()
		_, y = e.scr.CursorPosition()
		x = 0
	}

	if e.isModeSet(ansi.ModeInsertReplace) {
		// Insert mode [ansi.IRM]: the rest of the line shifts right by the
		// width of the new cell rather than being overwritten. Editors and
		// line editors drive single-character insertions through this instead
		// of redrawing the line.
		e.scr.insertCellsAt(x, y, cell.Width)
	}

	cellX, cellY := x, y
	e.scr.SetCell(x, y, &cell)

	// Handle phantom state at the end of the line
	e.atPhantom = awm && x+cell.Width >= scrWidth
	if !e.atPhantom {
		x += cell.Width
	}

	// NOTE: We don't reset the phantom state here, we handle it up above.
	e.scr.setCursor(x, y, false)

	cx, cy := e.scr.CursorPosition()
	e.printed = printedCell{
		scr:     e.scr,
		x:       cellX,
		y:       cellY,
		curX:    cx,
		curY:    cy,
		content: cell.Content,
		valid:   true,
	}
}
