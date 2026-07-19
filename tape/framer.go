package tape

// The framer is the completeness-by-construction half of the decoder. It knows
// the *shape* of every escape sequence ECMA-48 defines without knowing what any
// of them mean, so it can find where an unrecognised sequence ends and capture
// it verbatim as a Raw command.
//
// This is what makes a tape a faithful replay regardless of how much semantic
// protocol support exists. Recognising a sequence is a readability improvement;
// framing it is the correctness requirement. The reported bug, an APC reply to a
// kitty graphics query being shredded into three bogus keystrokes, was a framing
// failure: the old decoder knew only CSI and SS3 and treated ESC _ as the meta
// encoding of a literal underscore.

// frameResult says how the framer read the head of a buffer.
type frameResult int

const (
	// frameNone means the buffer does not start with ESC.
	frameNone frameResult = iota
	// frameIncomplete means the buffer ends inside a sequence.
	frameIncomplete
	// frameComplete means a whole sequence was framed.
	frameComplete
)

// frame returns the length of the escape sequence at the head of buf.
//
// A lone trailing ESC is reported complete with length 1, because at the point
// the framer is consulted the caller has already decided the chunk is over: a
// terminal delivers one keypress per read, so an ESC with nothing after it is
// the Escape key and not the start of a sequence that never arrived.
func frame(buf []byte) (n int, r frameResult) {
	if len(buf) == 0 || buf[0] != 0x1b {
		return 0, frameNone
	}
	if len(buf) == 1 {
		return 1, frameComplete // the Escape key
	}

	switch buf[1] {
	case '[':
		return frameCSI(buf)
	case ']', 'P', '_', '^', 'X':
		// OSC, DCS, APC, PM and SOS are control *strings*: an opening
		// introducer, arbitrary data, and a string terminator.
		return frameControlString(buf)
	case 'O', 'N':
		// SS3 and SS2 introduce exactly one byte.
		if len(buf) < 3 {
			return 0, frameIncomplete
		}
		return 3, frameComplete
	default:
		// ESC followed by anything else is a two-byte sequence: the meta
		// encoding of a key, or a standalone control like ESC \ (ST) or ESC =.
		// A multi-byte rune after ESC is handled by the caller, which owns the
		// meta-key reading; the framer only needs the extent.
		return 2, frameComplete
	}
}

// frameCSI reads a control sequence: ESC [, then parameter bytes (0x30-0x3f),
// then intermediate bytes (0x20-0x2f), then one final byte (0x40-0x7e).
//
// A byte outside those ranges aborts the sequence, and the framer reports the
// bytes up to and including the offender as complete. That is deliberate: a
// malformed sequence still has to be captured somewhere, and capturing it as
// Raw replays it byte for byte, which is exactly what the child sent.
func frameCSI(buf []byte) (int, frameResult) {
	i := 2
	for i < len(buf) && buf[i] >= 0x30 && buf[i] <= 0x3f {
		i++
	}
	for i < len(buf) && buf[i] >= 0x20 && buf[i] <= 0x2f {
		i++
	}
	if i >= len(buf) {
		return 0, frameIncomplete
	}
	// Whether the byte is a valid final or a stray, it ends the sequence.
	return i + 1, frameComplete
}

// frameControlString reads a control string: an introducer, a data body, and a
// terminator that is either ST (ESC \) or, for OSC, the BEL shorthand.
//
// C0 controls other than ESC also abort a control string. Without that, a
// terminator the child never sent would let a single malformed byte swallow the
// rest of the session's input into one enormous Raw command.
func frameControlString(buf []byte) (int, frameResult) {
	osc := buf[1] == ']'
	for i := 2; i < len(buf); i++ {
		switch {
		case buf[i] == 0x1b:
			if i+1 >= len(buf) {
				return 0, frameIncomplete
			}
			if buf[i+1] == '\\' {
				return i + 2, frameComplete // ST
			}
			// A nested ESC that is not ST aborts the string.
			return i, frameComplete
		case osc && buf[i] == 0x07:
			return i + 1, frameComplete // BEL terminates an OSC
		case buf[i] < 0x20 && buf[i] != 0x09 && buf[i] != 0x0a && buf[i] != 0x0d:
			// A C0 control aborts the string without being part of it.
			return i, frameComplete
		}
	}
	return 0, frameIncomplete
}
