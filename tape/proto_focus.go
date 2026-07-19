package tape

import "bytes"

func init() { Register(focusReporting{}) }

// focusReporting decodes mode 1004 focus events: CSI I when the terminal window
// gains focus and CSI O when it loses it.
//
// This protocol was written last, deliberately, as the check on the claim that
// adding an encoding is a self-contained change. It consists of this file and
// its tests. It does not touch the recorder, the framer, the decode loop, the
// player, or any other protocol; the Register call in its init is the whole of
// its wiring. The only edits outside this file are the ones any new command
// verb needs: a Kind, a line of grammar, and a line of printing.
type focusReporting struct{}

func (focusReporting) Name() string       { return "focus" }
func (focusReporting) Priority() int      { return 0 }
func (focusReporting) Fidelity() Fidelity { return Exact }

const (
	focusInSeq  = "\x1b[I"
	focusOutSeq = "\x1b[O"
)

func (focusReporting) Decode(buf []byte, _ Modes) (int, []Command, Result) {
	if len(buf) < 3 {
		if bytes.HasPrefix([]byte(focusInSeq), buf) || bytes.HasPrefix([]byte(focusOutSeq), buf) {
			return 0, nil, Partial
		}
		return 0, nil, NoMatch
	}
	switch string(buf[:3]) {
	case focusInSeq:
		return 3, []Command{{Kind: KindFocus, FocusIn: true}}, Full
	case focusOutSeq:
		return 3, []Command{{Kind: KindFocus, FocusIn: false}}, Full
	default:
		return 0, nil, NoMatch
	}
}

func (focusReporting) Encode(c Command, _ Modes) ([]byte, bool) {
	if c.Kind != KindFocus {
		return nil, false
	}
	if c.FocusIn {
		return []byte(focusInSeq), true
	}
	return []byte(focusOutSeq), true
}
