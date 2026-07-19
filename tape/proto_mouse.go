package tape

import (
	"github.com/Gaurav-Gosain/tuitest"
)

func init() {
	Register(mouseSGR{})
	Register(mouseX10{})
	Register(mouseURXVT{})
}

// Mouse reporting exists in several encodings that a program selects with a DEC
// private mode. Which *events* are reported (press only, press and drag, all
// motion) is chosen by modes 9, 1000, 1002 and 1003; which *bytes* carry them is
// chosen independently by modes 1006 (SGR), 1015 (urxvt) and 1016 (SGR pixel).
// The event-selection modes do not change the wire format, so the decoder only
// needs to tell the three encodings apart, and each is structurally distinct.
//
// Mode 1005 (UTF-8) is deliberately not decoded. Its reports are byte for byte
// indistinguishable from X10 reports whose coordinates happen to be non-ASCII,
// so any decoder that claimed to handle both would have to guess. It falls to
// the raw capture and still replays exactly.

// unpackButton reverses the control-byte packing shared by every encoding.
func unpackButton(cb int) (tuitest.MouseButton, tuitest.MouseAction, tuitest.KeyMods, bool) {
	var mods tuitest.KeyMods
	if cb&4 != 0 {
		mods |= tuitest.ModShift
	}
	if cb&8 != 0 {
		mods |= tuitest.ModAlt
	}
	if cb&16 != 0 {
		mods |= tuitest.ModCtrl
	}
	motion := cb&32 != 0

	var btn tuitest.MouseButton
	switch {
	case cb&128 != 0:
		switch cb & 3 {
		case 0:
			btn = tuitest.MouseBackward
		case 1:
			btn = tuitest.MouseForward
		default:
			// Buttons 10 and 11 have no name in this model, and inventing one
			// would make the tape lie about what was pressed.
			return 0, 0, 0, false
		}
	case cb&64 != 0:
		switch cb & 3 {
		case 0:
			btn = tuitest.MouseWheelUp
		case 1:
			btn = tuitest.MouseWheelDown
		case 2:
			btn = tuitest.MouseWheelLeft
		default:
			btn = tuitest.MouseWheelRight
		}
	default:
		switch cb & 3 {
		case 0:
			btn = tuitest.MouseLeft
		case 1:
			btn = tuitest.MouseMiddle
		case 2:
			btn = tuitest.MouseRight
		default:
			btn = tuitest.MouseNone
		}
	}

	action := tuitest.MousePress
	if motion {
		// The motion bit alone does not say whether a button is held; the
		// button field does. Splitting them here is what lets a drag read as a
		// drag in the tape instead of as a move that happens to name a button.
		if btn == tuitest.MouseNone {
			action = tuitest.MouseMove
		} else {
			action = tuitest.MouseDrag
		}
	}
	return btn, action, mods, true
}

// mouseSGR decodes the SGR encoding, mode 1006, and its pixel-coordinate
// variant, mode 1016. The two are byte for byte identical, so the pixel reading
// is taken only when the program actually enabled 1016.
type mouseSGR struct{}

func (mouseSGR) Name() string       { return "mouse-sgr" }
func (mouseSGR) Priority() int      { return 0 }
func (mouseSGR) Fidelity() Fidelity { return Exact }

// Keyboard reports false, so the registry rejects this protocol if it ever
// emits a Key or Type command. These bytes are reports from the terminal,
// never something the user pressed.
func (mouseSGR) Keyboard() bool { return false }

func (mouseSGR) Decode(buf []byte, m Modes) (int, []Command, Result) {
	if len(buf) < 3 || buf[0] != 0x1b || buf[1] != '[' {
		return 0, nil, NoMatch
	}
	if buf[2] != '<' {
		return 0, nil, NoMatch
	}
	params, n, done := scanParams(buf, 3)
	if !done {
		return 0, nil, Partial
	}
	final := buf[n-1]
	if final != 'M' && final != 'm' {
		return 0, nil, NoMatch
	}
	if len(params) != 3 {
		return 0, nil, NoMatch
	}

	btn, action, mods, ok := unpackButton(params[0])
	if !ok {
		return 0, nil, NoMatch
	}
	if final == 'm' {
		action = tuitest.MouseRelease
	}
	// Wire coordinates are 1-based. A zero would underflow to -1, which no
	// encoder can represent, so it is left to the raw capture.
	if params[1] < 1 || params[2] < 1 || !inTapeRange(params[1]-1, params[2]-1) {
		return 0, nil, NoMatch
	}

	ev := tuitest.MouseEvent{
		Col: params[1] - 1, Row: params[2] - 1,
		Button: btn, Action: action, Mods: mods,
		Pixel: m.DEC(1016),
		Enc:   tuitest.MouseSGR,
	}
	return n, []Command{{Kind: KindMouse, Mouse: ev}}, Full
}

func (mouseSGR) Encode(c Command, _ Modes) ([]byte, bool) {
	if c.Kind != KindMouse || c.Mouse.Enc != tuitest.MouseSGR {
		return nil, false
	}
	s, ok := c.Mouse.EncodeSGR()
	if !ok {
		return nil, false
	}
	return []byte(s), true
}

// mouseX10 decodes the original encoding, used by modes 9, 1000, 1002 and 1003:
// CSI M followed by exactly three bytes holding the control byte and the two
// coordinates, each offset by 32.
type mouseX10 struct{}

func (mouseX10) Name() string       { return "mouse-x10" }
func (mouseX10) Priority() int      { return 0 }
func (mouseX10) Fidelity() Fidelity { return Exact }

// Keyboard reports false, so the registry rejects this protocol if it ever
// emits a Key or Type command. These bytes are reports from the terminal,
// never something the user pressed.
func (mouseX10) Keyboard() bool { return false }

func (mouseX10) Decode(buf []byte, _ Modes) (int, []Command, Result) {
	if len(buf) < 3 || buf[0] != 0x1b || buf[1] != '[' {
		return 0, nil, NoMatch
	}
	if buf[2] != 'M' {
		return 0, nil, NoMatch
	}
	if len(buf) < 6 {
		// The three payload bytes are not all here yet. Reporting Partial is
		// what stops the framer from capturing a bare "CSI M" as a raw
		// sequence and leaving the payload to be decoded as typed text.
		return 0, nil, Partial
	}

	cb := int(buf[3]) - 32
	col := int(buf[4]) - 33
	row := int(buf[5]) - 33
	if cb < 0 || col < 0 || row < 0 {
		return 0, nil, NoMatch
	}
	btn, action, mods, ok := unpackButton(cb)
	if !ok {
		return 0, nil, NoMatch
	}
	// The encoding reports a release as the no-button code, so a release can be
	// recorded but the button that came up genuinely is not in the bytes.
	if btn == tuitest.MouseNone && action == tuitest.MousePress {
		action = tuitest.MouseRelease
	}

	ev := tuitest.MouseEvent{
		Col: col, Row: row,
		Button: btn, Action: action, Mods: mods,
		Enc: tuitest.MouseX10,
	}
	return 6, []Command{{Kind: KindMouse, Mouse: ev}}, Full
}

func (mouseX10) Encode(c Command, _ Modes) ([]byte, bool) {
	if c.Kind != KindMouse || c.Mouse.Enc != tuitest.MouseX10 {
		return nil, false
	}
	s, ok := c.Mouse.EncodeX10()
	if !ok {
		return nil, false
	}
	return []byte(s), true
}

// mouseURXVT decodes the urxvt encoding, mode 1015: the X10 control byte and
// 1-based coordinates written as three decimal parameters ending in M.
//
// Nothing else sends a three-parameter CSI M as *input*, and the control byte is
// always at least 32 because of the X10 offset, which together make the shape
// unambiguous on the input channel.
type mouseURXVT struct{}

func (mouseURXVT) Name() string       { return "mouse-urxvt" }
func (mouseURXVT) Priority() int      { return 0 }
func (mouseURXVT) Fidelity() Fidelity { return Exact }

// Keyboard reports false, so the registry rejects this protocol if it ever
// emits a Key or Type command. These bytes are reports from the terminal,
// never something the user pressed.
func (mouseURXVT) Keyboard() bool { return false }

func (mouseURXVT) Decode(buf []byte, _ Modes) (int, []Command, Result) {
	if len(buf) < 3 || buf[0] != 0x1b || buf[1] != '[' {
		return 0, nil, NoMatch
	}
	if buf[2] < '0' || buf[2] > '9' {
		return 0, nil, NoMatch
	}
	params, n, done := scanParams(buf, 2)
	if !done {
		return 0, nil, Partial
	}
	if buf[n-1] != 'M' || len(params) != 3 {
		return 0, nil, NoMatch
	}

	cb := params[0] - 32
	if cb < 0 || params[1] < 1 || params[2] < 1 || !inTapeRange(params[1]-1, params[2]-1) {
		return 0, nil, NoMatch
	}
	btn, action, mods, ok := unpackButton(cb)
	if !ok {
		return 0, nil, NoMatch
	}
	if btn == tuitest.MouseNone && action == tuitest.MousePress {
		action = tuitest.MouseRelease
	}

	ev := tuitest.MouseEvent{
		Col: params[1] - 1, Row: params[2] - 1,
		Button: btn, Action: action, Mods: mods,
		Enc: tuitest.MouseURXVT,
	}
	return n, []Command{{Kind: KindMouse, Mouse: ev}}, Full
}

func (mouseURXVT) Encode(c Command, _ Modes) ([]byte, bool) {
	if c.Kind != KindMouse || c.Mouse.Enc != tuitest.MouseURXVT {
		return nil, false
	}
	s, ok := c.Mouse.EncodeURXVT()
	if !ok {
		return nil, false
	}
	return []byte(s), true
}

// scanParams reads the semicolon-separated decimal parameters of a CSI sequence
// starting at index start, and returns them with the total length of the
// sequence including its final byte.
//
// done is false when the buffer ends before the final byte, which the caller
// reports as Partial. Parameters are capped so that a long run of digits cannot
// overflow into a nonsense coordinate; an overflowing parameter yields ok=false
// via a negative value, which every caller range-checks.
func scanParams(buf []byte, start int) (params []int, n int, done bool) {
	cur := -1
	for i := start; i < len(buf); i++ {
		b := buf[i]
		switch {
		case b >= '0' && b <= '9':
			if cur < 0 {
				cur = 0
			}
			if cur <= maxParam {
				cur = cur*10 + int(b-'0')
			}
		case b == ';':
			params = append(params, cur)
			cur = -1
		case b >= 0x40 && b <= 0x7e:
			if cur >= 0 || len(params) > 0 {
				params = append(params, cur)
			}
			return params, i + 1, true
		default:
			// Not a parameter or a final byte, so this is not a sequence any
			// mouse encoding produces.
			return nil, i + 1, true
		}
	}
	return nil, 0, false
}

// maxParam bounds a decoded CSI parameter. It is far above any real coordinate
// and well below the point where the accumulator could overflow.
const maxParam = 1 << 20

// inTapeRange reports whether a decoded coordinate is one a Mouse line can
// actually carry. The decoder has to agree with the parser here: emitting a
// coordinate the parser would reject would produce a recording that does not
// parse, so an out-of-range report is declined and captured raw instead.
func inTapeRange(col, row int) bool {
	return col >= 0 && row >= 0 && col < MaxDimension && row < MaxDimension
}
