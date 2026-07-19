package fuzz

import (
	"math/rand/v2"
	"strings"
)

// Hostile byte sequences fed to the program under test as input. A TUI parses
// its own stdin looking for key and mouse escape sequences, so that parser is
// an attack surface in exactly the way a network protocol parser is: it sees
// bytes it did not produce and must not fall over on them. These are the shapes
// that break hand-written input parsers.
//
// Each entry is a complete, self-contained input burst. The generator also
// synthesizes parameterised variants (see hostileGenerated) whose sizes scale
// with the PRNG, since a fixed table cannot cover "how big is too big".
var hostileFixed = []string{
	// Truncated escape sequences: a parser that blocks waiting for more bytes,
	// or indexes past the end of its buffer, breaks here.
	"\x1b",
	"\x1b[",
	"\x1b]",
	"\x1bO",
	"\x1b[<",
	"\x1b[1;",
	"\x1b[?",
	"\x1bP",
	"\x1b_",
	"\x1b^",
	"\x1bX",

	// Escape sequences with a missing or wrong final byte.
	"\x1b[1;2",
	"\x1b[999",
	"\x1b[<0;1;1",
	"\x1b[200~",          // paste start with no matching end
	"\x1b[201~",          // paste end with no start
	"\x1b[200~\x1b[200~", // nested paste starts

	// Unterminated string sequences. A parser that accumulates until the
	// terminator will buffer without bound.
	"\x1b]0;unterminated title",
	"\x1b]52;c;",
	"\x1bPunterminated dcs",
	"\x1b_unterminated apc",
	"\x1b^unterminated pm",

	// Malformed UTF-8. Bare continuation bytes, truncated multibyte starts,
	// overlong encodings, surrogate halves, and out-of-range lead bytes.
	"\x80",
	"\xbf",
	"\xc0",
	"\xc2",                 // 2-byte lead with no continuation
	"\xe2\x82",             // truncated 3-byte
	"\xf0\x9f\x92",         // truncated 4-byte
	"\xc0\xaf",             // overlong solidus
	"\xe0\x80\xaf",         // overlong 3-byte
	"\xf0\x80\x80\xaf",     // overlong 4-byte
	"\xed\xa0\x80",         // UTF-16 surrogate half
	"\xf8\x88\x80\x80\x80", // 5-byte sequence, not valid UTF-8
	"\xfe\xff",
	"\xff\xfe",

	// Control characters a TUI rarely exercises deliberately.
	"\x00",
	"\x01\x02\x03\x04",
	"\x07",
	"\x08\x08\x08",
	"\x0b\x0c",
	"\x0e\x0f", // shift out / shift in
	"\x1a",
	"\x7f\x7f\x7f",

	// C1 control bytes sent raw, which some parsers treat as escape starters.
	"\x9b",
	"\x9d",
	"\x90",

	// Mixed: a valid prefix followed by garbage, to catch parsers that commit
	// to a state machine branch and never recover.
	"\x1b[A\x1b[",
	"\x1b[<0;1;1M\xc3",
	"\x1b[1;1H\x80\x80",
}

// hostileGenerated builds a size-parameterised hostile burst. These cover the
// "enormous" cases a fixed table cannot: parameter counts and numeric values
// that overflow fixed-size buffers or int parsing.
func hostileGenerated(r *rand.Rand) string {
	switch r.IntN(8) {
	case 0:
		// Enormous parameter count in a CSI sequence.
		n := 64 + r.IntN(4096)
		var b strings.Builder
		b.WriteString("\x1b[")
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteString("1")
		}
		b.WriteByte('m')
		return b.String()

	case 1:
		// Numeric parameter far beyond what fits in an int32 or int64.
		digits := 10 + r.IntN(40)
		return "\x1b[" + strings.Repeat("9", digits) + "H"

	case 2:
		// Very long OSC string with no terminator.
		return "\x1b]0;" + strings.Repeat("A", 256+r.IntN(8192))

	case 3:
		// Very long DCS payload with no terminator.
		return "\x1bP" + strings.Repeat("q", 256+r.IntN(8192))

	case 4:
		// Long run of intermediate bytes before a final byte.
		return "\x1b[" + strings.Repeat("!", 32+r.IntN(512)) + "p"

	case 5:
		// A dense stream of random bytes: pure noise, as a control against the
		// structured cases.
		n := 16 + r.IntN(1024)
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.IntN(256))
		}
		return string(b)

	case 6:
		// Many escape starts back to back, so the parser never completes one.
		return strings.Repeat("\x1b", 32+r.IntN(512))

	default:
		// A long run of continuation bytes, which is never valid UTF-8 and
		// tends to expose rune-decoding loops that fail to advance.
		return strings.Repeat("\x80", 32+r.IntN(1024))
	}
}

// hostile returns a hostile input burst, mixing the fixed table with generated
// variants so both known-bad shapes and size extremes get covered.
func hostile(r *rand.Rand) string {
	if r.IntN(3) == 0 {
		return hostileGenerated(r)
	}
	return hostileFixed[r.IntN(len(hostileFixed))]
}
