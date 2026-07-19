package tape

// The framer is the guarantee that a recording cannot lose input. It knows the
// ECMA-48 shape of a control sequence without knowing what any sequence means,
// so it can find where an unrecognized sequence ends and capture it verbatim as
// a Raw command. Protocol support is layered on top of this purely to make
// tapes readable; it is never load bearing for fidelity.
//
// This is why the recorder no longer has a "dropped" counter. There is nothing
// to drop: every byte is either decoded by a protocol or framed into Raw.

// C1 control introducers in their 8-bit form. A byte in 0x80-0x9f can never
// start a valid UTF-8 rune, so encountering one at a rune boundary is
// unambiguously C1 rather than text.
const (
	c1SOS = 0x98
	c1CSI = 0x9b
	c1ST  = 0x9c
	c1OSC = 0x9d
	c1PM  = 0x9e
	c1APC = 0x9f
	c1DCS = 0x90
	c1SS2 = 0x8e
	c1SS3 = 0x8f
)

// frameEnd returns the length of the control sequence at the head of buf.
//
// complete is false when buf holds only a proper prefix and more bytes could
// finish it, in which case the caller must hold the bytes rather than decode a
// truncated sequence. ok is false when buf does not start a control sequence at
// all, which leaves it to the keyboard protocols to interpret (a bare ESC is
// the Esc key, ESC plus a printable rune is the meta encoding of Alt).
func frameEnd(buf []byte) (n int, complete, ok bool) {
	if len(buf) == 0 {
		return 0, false, false
	}

	// 8-bit C1 introducers.
	switch buf[0] {
	case c1CSI:
		return csiBody(buf, 1)
	case c1OSC, c1DCS, c1APC, c1PM, c1SOS:
		return stringBody(buf, 1, buf[0] == c1OSC)
	case c1SS2, c1SS3:
		return singleShift(buf, 1)
	}

	if buf[0] != 0x1b {
		return 0, false, false
	}
	if len(buf) < 2 {
		// A lone ESC is the Esc key, not an incomplete sequence. Deciding
		// otherwise would make the Esc key un-recordable.
		return 0, false, false
	}

	switch buf[1] {
	case '[':
		return csiBody(buf, 2)
	case ']':
		return stringBody(buf, 2, true)
	case 'P', '_', '^', 'X':
		return stringBody(buf, 2, false)
	case 'N', 'O':
		return singleShift(buf, 2)
	case '\\':
		// A lone string terminator. A well formed control string is consumed
		// whole by stringBody and its terminator never arrives here, but a
		// string closed by the 8-bit terminator leaves the 7-bit spelling
		// stranded. It is a control, not text, so it is framed as one two byte
		// sequence and recorded as a single Raw rather than splitting into a
		// bare ESC and a literal backslash.
		return 2, true, true
	}
	return 0, false, false
}

// csiBody finds the end of a CSI sequence whose parameter bytes start at i.
// The grammar is parameter bytes 0x30-0x3f, then intermediate bytes 0x20-0x2f,
// then one final byte 0x40-0x7e.
func csiBody(buf []byte, i int) (n int, complete, ok bool) {
	for ; i < len(buf); i++ {
		b := buf[i]
		switch {
		case b >= 0x30 && b <= 0x3f, b >= 0x20 && b <= 0x2f:
			// Parameter or intermediate byte; keep scanning.
		case b >= 0x40 && b <= 0x7e:
			return i + 1, true, true
		default:
			// An illegal byte aborts the sequence. Frame what came
			// before it so the bad bytes still round-trip as Raw
			// rather than being silently absorbed.
			return i, true, true
		}
	}
	return 0, false, true
}

// singleShift frames SS2/SS3, which take exactly one following byte.
func singleShift(buf []byte, i int) (n int, complete, ok bool) {
	if i >= len(buf) {
		return 0, false, true
	}
	return i + 1, true, true
}

// stringBody finds the end of a control string (OSC, DCS, APC, PM, SOS). These
// run until a string terminator: ST, either ESC \ or the 8-bit 0x9c, and for
// OSC additionally BEL, which xterm accepts and many programs emit.
func stringBody(buf []byte, i int, allowBEL bool) (n int, complete, ok bool) {
	for ; i < len(buf); i++ {
		switch buf[i] {
		case 0x07:
			if allowBEL {
				return i + 1, true, true
			}
		case c1ST:
			return i + 1, true, true
		case 0x1b:
			if i+1 >= len(buf) {
				return 0, false, true
			}
			if buf[i+1] == '\\' {
				return i + 2, true, true
			}
			// An ESC that is not ST aborts the string, which is how
			// terminals recover from a truncated one.
			return i, true, true
		}
	}
	return 0, false, true
}
