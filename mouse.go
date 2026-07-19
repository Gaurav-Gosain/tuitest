package tuitest

import "fmt"

// MouseButton identifies a mouse button or wheel direction.
type MouseButton int

const (
	// MouseLeft is the primary button.
	MouseLeft MouseButton = iota
	// MouseMiddle is the middle button or wheel click.
	MouseMiddle
	// MouseRight is the secondary button.
	MouseRight
	// MouseWheelUp is one wheel notch away from the user.
	MouseWheelUp
	// MouseWheelDown is one wheel notch toward the user.
	MouseWheelDown
	// MouseWheelLeft is one horizontal wheel notch to the left.
	MouseWheelLeft
	// MouseWheelRight is one horizontal wheel notch to the right.
	MouseWheelRight
	// MouseBackward is the fourth button, "back" on most mice.
	MouseBackward
	// MouseForward is the fifth button, "forward" on most mice.
	MouseForward
	// MouseNone is no button: the button field of a motion report with nothing
	// held, and of a legacy release report, which does not say which button
	// came up.
	MouseNone
)

// MouseAction is what the button did.
type MouseAction int

const (
	// MousePress is a button going down.
	MousePress MouseAction = iota
	// MouseRelease is a button coming up.
	MouseRelease
	// MouseMove is motion with no button held.
	MouseMove
	// MouseDrag is motion with a button held. On the wire it is the same
	// motion bit as MouseMove; the two are distinguished by whether a button
	// is reported, and separating them is what lets a tape read as a drag.
	MouseDrag
)

// MouseEncoding is the wire format a mouse report used.
//
// It is part of the event because replaying a recorded session has to send the
// program the same bytes it originally received: a program that enabled only
// mode 1000 does not understand an SGR report, so re-encoding a captured X10
// report as SGR would silently change what the test exercises.
type MouseEncoding int

const (
	// MouseSGR is the modern SGR encoding (mode 1006), and the default for
	// events constructed by hand. It is the only encoding with no coordinate
	// limit and the only one that reports which button was released.
	MouseSGR MouseEncoding = iota
	// MouseX10 is the original encoding (modes 9, 1000, 1002 and 1003), which
	// packs each field into one byte offset by 32.
	MouseX10
	// MouseURXVT is the urxvt encoding (mode 1015): X10's packing written as
	// decimal parameters, so it has no coordinate limit but still cannot say
	// which button was released.
	MouseURXVT
)

// KeyMods is a bitmask of held modifier keys.
type KeyMods int

const (
	// ModShift is the shift key.
	ModShift KeyMods = 1 << iota
	// ModAlt is the alt or meta key.
	ModAlt
	// ModCtrl is the control key.
	ModCtrl
)

// MouseEvent is a single mouse event at a zero-based cell coordinate.
type MouseEvent struct {
	// Col and Row are zero-based cell coordinates; the wire format's 1-based
	// coordinates are produced during encoding. When Pixel is set they are
	// zero-based pixel offsets instead.
	Col, Row int
	// Button is the button or wheel direction involved.
	Button MouseButton
	// Action is what the button did.
	Action MouseAction
	// Mods are the modifier keys held at the time.
	Mods KeyMods
	// Pixel reports coordinates in pixels rather than cells (mode 1016). The
	// wire format is identical to SGR's, so this can only ever be known from
	// whether the program asked for pixel reporting.
	Pixel bool
	// Enc is the wire encoding to use. The zero value is MouseSGR.
	Enc MouseEncoding
}

// controlByte packs the button, action and modifiers into the value every mouse
// encoding shares. All three encodings differ only in how they carry this byte
// and the coordinates, so the packing lives in one place.
//
// It returns false for a combination the wire format cannot express.
func (e MouseEvent) controlByte() (int, bool) {
	var cb int
	switch e.Button {
	case MouseLeft:
		cb = 0
	case MouseMiddle:
		cb = 1
	case MouseRight:
		cb = 2
	case MouseNone:
		cb = 3
	case MouseWheelUp:
		cb = 64
	case MouseWheelDown:
		cb = 65
	case MouseWheelLeft:
		cb = 66
	case MouseWheelRight:
		cb = 67
	case MouseBackward:
		cb = 128
	case MouseForward:
		cb = 129
	default:
		return 0, false
	}

	switch e.Action {
	case MouseMove:
		// Motion with nothing held. A caller that set a button anyway is
		// writing a non-canonical spelling of MouseDrag; the button is dropped
		// rather than silently turning the event into a drag on the wire.
		cb = 3 | 32
	case MouseDrag:
		if e.Button == MouseNone {
			return 0, false
		}
		cb |= 32
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
	return cb, true
}

// EncodeSGR renders the event as an SGR (1006) or SGR-pixel (1016) sequence.
// Coordinates in the wire format are 1-based. It reports false for an event the
// encoding cannot express.
func (e MouseEvent) EncodeSGR() (string, bool) {
	cb, ok := e.controlByte()
	if !ok {
		return "", false
	}
	if e.Col < 0 || e.Row < 0 {
		return "", false
	}
	final := 'M'
	if e.Action == MouseRelease {
		final = 'm'
	}
	return fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, e.Col+1, e.Row+1, final), true
}

// EncodeX10 renders the event in the original encoding, where the control byte
// and both coordinates are single bytes offset by 32.
//
// It reports false for anything that packing cannot hold: a coordinate past
// column 223, which has no representation at all, and a release, which the
// encoding reports as the button-3 code without saying which button came up.
// Falling back rather than approximating is what keeps a recorded session's
// bytes intact.
func (e MouseEvent) EncodeX10() (string, bool) {
	cb, ok := e.controlByte()
	if !ok {
		return "", false
	}
	if !legacyButtonIsRepresentable(e) {
		return "", false
	}
	if e.Col < 0 || e.Row < 0 || e.Col > maxX10Coord || e.Row > maxX10Coord {
		return "", false
	}
	// Built byte by byte rather than with %c, which would UTF-8 encode any
	// value above 127 into two bytes and corrupt the report.
	return string([]byte{0x1b, '[', 'M', byte(cb + 32), byte(e.Col + 33), byte(e.Row + 33)}), true
}

// legacyButtonIsRepresentable reports whether the X10 and urxvt encodings can
// express which button an event concerns.
//
// Both spend the same code, button 3, on "no button", and use it for every
// release regardless of which button came up. Two events therefore collide on
// the wire: a release naming a button, and a press naming none. Neither can be
// encoded without the reader getting the other one back, so both are declined
// and captured raw instead of being approximated.
func legacyButtonIsRepresentable(e MouseEvent) bool {
	if e.Action == MouseRelease && e.Button != MouseNone {
		return false
	}
	if e.Action == MousePress && e.Button == MouseNone {
		return false
	}
	return true
}

// maxX10Coord is the largest zero-based coordinate the X10 encoding can carry:
// the field is one byte offset by 33, so 222 lands on 255 and anything beyond
// has no representation on the wire at all.
const maxX10Coord = 222

// EncodeURXVT renders the event in the urxvt encoding (mode 1015): X10's
// control byte and 1-based coordinates written as decimal parameters, which
// lifts X10's coordinate limit but keeps its inability to name a released
// button.
func (e MouseEvent) EncodeURXVT() (string, bool) {
	cb, ok := e.controlByte()
	if !ok {
		return "", false
	}
	if !legacyButtonIsRepresentable(e) {
		return "", false
	}
	if e.Col < 0 || e.Row < 0 {
		return "", false
	}
	return fmt.Sprintf("\x1b[%d;%d;%dM", cb+32, e.Col+1, e.Row+1), true
}

// Encode renders the event in whichever wire encoding Enc names.
func (e MouseEvent) Encode() (string, bool) {
	switch e.Enc {
	case MouseX10:
		return e.EncodeX10()
	case MouseURXVT:
		return e.EncodeURXVT()
	default:
		return e.EncodeSGR()
	}
}

// SendMouse encodes a mouse event and sends it to the child, using the wire
// encoding named by the event's Enc field. The program under test must have
// enabled the matching mouse reporting mode for it to react.
func (t *Terminal) SendMouse(ev MouseEvent) error {
	s, ok := ev.Encode()
	if !ok {
		return fmt.Errorf("tuitest: mouse event %+v has no representation in its wire encoding", ev)
	}
	return t.write([]byte(s))
}
