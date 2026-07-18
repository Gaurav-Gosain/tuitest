package tuitest

import "fmt"

// MouseButton identifies a mouse button or wheel direction.
type MouseButton int

const (
	MouseLeft MouseButton = iota
	MouseMiddle
	MouseRight
	MouseWheelUp
	MouseWheelDown
)

// MouseAction is what the button did.
type MouseAction int

const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMove
)

// KeyMods is a bitmask of held modifier keys.
type KeyMods int

const (
	ModShift KeyMods = 1 << iota
	ModAlt
	ModCtrl
)

// MouseEvent is a single mouse event at a zero-based cell coordinate.
type MouseEvent struct {
	Col, Row int
	Button   MouseButton
	Action   MouseAction
	Mods     KeyMods
}

// encodeSGR renders the event as an SGR (1006) mouse sequence. Coordinates in
// the wire format are 1-based.
func (e MouseEvent) encodeSGR() string {
	var cb int
	switch e.Button {
	case MouseLeft:
		cb = 0
	case MouseMiddle:
		cb = 1
	case MouseRight:
		cb = 2
	case MouseWheelUp:
		cb = 64
	case MouseWheelDown:
		cb = 65
	}
	if e.Action == MouseMove {
		cb += 32
	}
	if e.Mods&ModShift != 0 {
		cb += 4
	}
	if e.Mods&ModAlt != 0 {
		cb += 8
	}
	if e.Mods&ModCtrl != 0 {
		cb += 16
	}
	final := 'M'
	if e.Action == MouseRelease {
		final = 'm'
	}
	return fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, e.Col+1, e.Row+1, final)
}

// SendMouse encodes an SGR mouse event and sends it to the child. The program
// under test must have enabled SGR mouse reporting (mode 1006) for it to react.
func (t *Terminal) SendMouse(ev MouseEvent) error {
	return t.write([]byte(ev.encodeSGR()))
}
