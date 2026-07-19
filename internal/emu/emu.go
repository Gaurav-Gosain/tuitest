// Package emu holds the VT emulator adapter used by tuitest. It defines a
// narrow Emulator interface and a single implementation backed by the copied
// vt package (which itself sits on top of charmbracelet/ultraviolet). Keeping
// this internal means the emulator choice is not part of tuitest's public
// contract and can be swapped without a breaking change.
package emu

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/Gaurav-Gosain/tuitest/internal/vt"
)

// Emulator is the minimal surface tuitest needs from a VT interpreter: feed it
// bytes, resize it, and read back the cell grid, cursor, and (optionally)
// OSC 133 semantic markers. Everything above this interface in tuitest is
// written against it, so the concrete emulator is replaceable.
type Emulator interface {
	// Write feeds output bytes (from the child PTY) into the emulator.
	Write(p []byte) (int, error)
	// Resize changes the emulator's grid size in cells.
	Resize(cols, rows int)
	// Size returns the current grid size in cells.
	Size() (cols, rows int)
	// CellAt returns the cell at the given zero-based column and row, or nil
	// when out of bounds.
	CellAt(col, row int) *uv.Cell
	// Cursor returns the cursor position (zero-based) and whether it is visible.
	Cursor() (col, row int, visible bool)
	// PromptCount returns how many OSC 133 prompt-start (A) markers have been
	// seen, used to detect a newly drawn shell prompt.
	PromptCount() int
	// CommandFinishedCount returns how many OSC 133 command-finished (D) markers
	// have been seen, used to detect a command completing.
	CommandFinishedCount() int
	// LastCommandExit returns the exit code from the most recent command-finished
	// marker, and whether any such marker exists.
	LastCommandExit() (code int, ok bool)
	// Modes reports the private modes currently set, keyed by mode number
	// (1049 alt screen, 1000/1002/1003 mouse tracking, 2004 bracketed paste,
	// and so on). Only modes that are set appear in the map. It exists so
	// callers can tell whether a program restored the terminal on exit.
	Modes() map[int]bool
}

// New builds the default ultraviolet/vt-backed emulator at the given size.
func New(cols, rows int) Emulator {
	return &adapter{e: vt.NewEmulator(cols, rows)}
}

type adapter struct {
	e *vt.Emulator
}

func (a *adapter) Write(p []byte) (int, error) { return a.e.Write(p) }

func (a *adapter) Resize(cols, rows int) { a.e.Resize(cols, rows) }

func (a *adapter) Size() (int, int) { return a.e.Width(), a.e.Height() }

func (a *adapter) CellAt(col, row int) *uv.Cell { return a.e.CellAt(col, row) }

func (a *adapter) Cursor() (int, int, bool) {
	p := a.e.CursorPosition()
	return p.X, p.Y, !a.e.IsCursorHidden()
}

func (a *adapter) PromptCount() int {
	return a.count(vt.MarkerPromptStart)
}

func (a *adapter) CommandFinishedCount() int {
	return a.count(vt.MarkerCommandFinished)
}

func (a *adapter) count(t vt.SemanticMarkerType) int {
	n := 0
	for _, m := range a.e.SemanticMarkers().Markers() {
		if m.Type == t {
			n++
		}
	}
	return n
}

func (a *adapter) Modes() map[int]bool { return a.e.GetModes() }

func (a *adapter) LastCommandExit() (int, bool) {
	m := a.e.SemanticMarkers().Last(vt.MarkerCommandFinished)
	if m == nil {
		return 0, false
	}
	return m.ExitCode, true
}
