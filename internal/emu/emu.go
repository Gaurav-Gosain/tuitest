// Package emu holds the VT emulator adapter used by tuitest. It defines a
// narrow Emulator interface and a single implementation backed by the copied
// vt package (which itself sits on top of charmbracelet/ultraviolet). Keeping
// this internal means the emulator choice is not part of tuitest's public
// contract and can be swapped without a breaking change.
package emu

import (
	"bytes"
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/Gaurav-Gosain/tuitest/internal/vt"
)

// Emulator is the minimal surface tuitest needs from a VT interpreter: feed it
// bytes, resize it, and read back the cell grid, cursor, and (optionally)
// OSC 133 semantic markers. Everything above this interface in tuitest is
// written against it, so the concrete emulator is replaceable.
type Emulator interface {
	// Write feeds output bytes (from the child PTY) into the emulator.
	Write(p []byte) (int, error)
	// TakeResponses removes and returns the bytes the emulator wants to send
	// back to the program, the answers to queries it made: cursor position
	// reports, foreground and background colour queries, device attributes,
	// and keyboard protocol probes. A real terminal answers these, so the
	// harness must too, or any program that probes before drawing hangs on a
	// retry loop and paints nothing. Callers drain after each Write and after
	// each Resize, and forward the result to the PTY. It must not block, and
	// returns nil when nothing is queued.
	TakeResponses() []byte
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
	// seen, used to detect a newly drawn shell prompt. It only ever grows:
	// clearing the screen or trimming scrollback does not lower it.
	PromptCount() int
	// CommandFinishedCount returns how many OSC 133 command-finished (D) markers
	// have been seen, used to detect a command completing. Like PromptCount it
	// only ever grows.
	CommandFinishedCount() int
	// LastCommandExit returns the exit code from the most recent command-finished
	// marker, and whether any such marker exists. A marker that carried no code
	// reports -1.
	LastCommandExit() (code int, ok bool)
	// Modes reports the DEC private modes currently set, keyed by mode number
	// (47, 1047 and 1049 alt screen, 1000/1002/1003 mouse tracking, 2004
	// bracketed paste, and so on). Only modes that are set appear in the map.
	// It exists so callers can tell whether a program restored the terminal on
	// exit.
	Modes() map[int]bool
	// ApplicationCursorKeys reports whether DECCKM (mode 1) is set, in which
	// case a terminal sends the cursor keys as SS3 sequences (ESC O A) instead
	// of CSI sequences (ESC [ A).
	ApplicationCursorKeys() bool
	// OnSync registers fn to be called, from inside Write, whenever a
	// synchronized update (DEC mode 2026) opens or closes. It is called with
	// true when the program begins a frame, before any of the frame's bytes
	// reach the grid, and with false when the program ends it. Opening an
	// update that is already open does not call fn again.
	OnSync(fn func(open bool))
}

// New builds the default ultraviolet/vt-backed emulator at the given size.
func New(cols, rows int) Emulator {
	a := &adapter{e: vt.NewEmulator(cols, rows), modes: map[int]bool{}, lastExit: -1}
	// The vt package ran its own mode reset before these callbacks existed,
	// so the modes it set by default are copied in once here. From now on the
	// callbacks see every change. GetModes also lists the modes it knows that
	// are reset, as false, and those must not be copied: Modes reports every
	// key in the map as set.
	for mode, set := range a.e.GetModes() {
		if set {
			a.modes[mode] = true
		}
	}
	if !a.e.IsCursorHidden() {
		a.modes[int(ansi.ModeTextCursorEnable.Mode())] = true
	}
	a.e.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { a.modeChanged(m, true) },
		DisableMode: func(m ansi.Mode) { a.modeChanged(m, false) },
		AltScreen:   a.altScreenChanged,
	})
	// Registered after the emulator's own handler, so it runs first. It
	// returns false, so the emulator's handler still records the marker for
	// everything else that reads it.
	a.e.RegisterOscHandler(133, a.semanticMarker)
	return a
}

type adapter struct {
	e *vt.Emulator

	// modes mirrors every DEC private mode the program has set or reset, fed
	// by the emulator's mode callbacks. vt's own GetModes lists only the modes
	// tuios needs to restore a session, which leaves out 47 (the original
	// alternate screen) and 2026, among others.
	modes map[int]bool

	syncOpen bool
	onSync   func(open bool)

	// Marker counters are kept here rather than read from the emulator's
	// marker list, because that list is not a history. It drops the markers on
	// screen when the screen is cleared and all of them on ED 3, so counting it
	// can go down, and a wait for "one more prompt than before" then never
	// ends. A shell that runs `clear` does exactly that.
	prompts  int
	finished int
	lastExit int
	anyExit  bool
}

func (a *adapter) modeChanged(m ansi.Mode, set bool) {
	dec, ok := m.(ansi.DECMode)
	if !ok {
		return // an ANSI mode; its numbers overlap the DEC ones
	}
	n := int(dec)
	if set {
		a.modes[n] = true
	} else {
		delete(a.modes, n)
	}
	if dec == ansi.ModeSynchronizedOutput && set != a.syncOpen {
		a.syncOpen = set
		if a.onSync != nil {
			a.onSync(set)
		}
	}
}

// altScreenChanged forgets mode 47 when the main screen comes back. 47 is not
// among the modes a full reset walks, so without this a RIS issued while 47 was
// set would leave it recorded as set on a terminal showing the main screen.
func (a *adapter) altScreenChanged(on bool) {
	if !on {
		delete(a.modes, 47)
	}
}

// semanticMarker counts OSC 133 prompt-start and command-finished markers. The
// data is the whole OSC payload, "133;A" or "133;D;0" and so on.
func (a *adapter) semanticMarker(data []byte) bool {
	parts := bytes.Split(data, []byte{';'})
	if len(parts) < 2 || len(parts[1]) == 0 {
		return false
	}
	switch parts[1][0] {
	case 'A':
		a.prompts++
	case 'D':
		a.finished++
		a.anyExit = true
		a.lastExit = -1
		if len(parts) >= 3 {
			if code, err := strconv.Atoi(string(parts[2])); err == nil {
				a.lastExit = code
			}
		}
	}
	return false
}

func (a *adapter) Write(p []byte) (int, error) { return a.e.Write(p) }

func (a *adapter) TakeResponses() []byte { return a.e.TakeResponses() }

func (a *adapter) Resize(cols, rows int) { a.e.Resize(cols, rows) }

func (a *adapter) Size() (int, int) { return a.e.Width(), a.e.Height() }

func (a *adapter) CellAt(col, row int) *uv.Cell { return a.e.CellAt(col, row) }

func (a *adapter) Cursor() (int, int, bool) {
	p := a.e.CursorPosition()
	return p.X, p.Y, !a.e.IsCursorHidden()
}

func (a *adapter) PromptCount() int { return a.prompts }

func (a *adapter) CommandFinishedCount() int { return a.finished }

func (a *adapter) LastCommandExit() (int, bool) {
	if !a.anyExit {
		return 0, false
	}
	return a.lastExit, true
}

func (a *adapter) Modes() map[int]bool {
	out := make(map[int]bool, len(a.modes))
	for n := range a.modes {
		out[n] = true
	}
	return out
}

func (a *adapter) ApplicationCursorKeys() bool { return a.e.ApplicationCursorKeys() }

func (a *adapter) OnSync(fn func(open bool)) { a.onSync = fn }
