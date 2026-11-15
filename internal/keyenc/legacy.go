package keyenc

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Result reports how much of a buffer a decoder recognized.
type Result int

const (
	// NoMatch means the bytes are not a key this decoder knows.
	NoMatch Result = iota
	// Partial means the bytes are a proper prefix of a key and more input
	// could complete it.
	Partial
	// Full means the decoder consumed a whole key.
	Full
)

// CSIFramer frames a CSI sequence at the head of buf into its parameter body
// and final byte.
type CSIFramer func(buf []byte) (body []byte, final byte, n int, r Result)

// FrameCSI is the plain CSI framer: parameter and intermediate bytes, then one
// final byte. Anything else makes it NoMatch.
func FrameCSI(buf []byte) ([]byte, byte, int, Result) {
	if len(buf) < 2 || buf[0] != 0x1b || buf[1] != '[' {
		return nil, 0, 0, NoMatch
	}
	for i := 2; i < len(buf); i++ {
		switch b := buf[i]; {
		case b >= 0x20 && b <= 0x3f:
		case b >= 0x40 && b <= 0x7e:
			return buf[2:i], b, i + 1, Full
		default:
			return nil, 0, 0, NoMatch
		}
	}
	return nil, 0, 0, Partial
}

// DecodeLegacy reads one key in the legacy encoding at the head of buf: a C0
// control byte or DEL, the ESC-prefixed meta encoding of Alt, or a CSI or SS3
// cursor or function key with its xterm modifier parameter. It returns the key
// as a tape token, such as "Ctrl+b" or "Alt+Up". frame frames CSI sequences,
// and nil means FrameCSI.
//
// A printable rune on its own is text, not a key, and is NoMatch. A lone ESC
// at the end of buf is Partial, since more input could make it the start of a
// longer key.
func DecodeLegacy(buf []byte, frame CSIFramer) (int, string, Result) {
	if len(buf) == 0 {
		return 0, "", NoMatch
	}

	// C0 controls and DEL are keys in their own right.
	if buf[0] < 0x20 && buf[0] != 0x1b || buf[0] == 0x7f {
		return 1, C0Token(buf[0]), Full
	}
	if buf[0] != 0x1b {
		return 0, "", NoMatch
	}
	if len(buf) == 1 {
		// A lone ESC at the end of the buffer says nothing about what it
		// is. The kernel may end a read anywhere, including between the
		// ESC and the rest of an arrow key, so deciding here would turn a
		// split arrow key into the Esc key followed by literal "[A".
		//
		// It is reported incomplete instead, and the caller resolves it
		// as the Esc key once it knows no more bytes are coming. That is
		// also what a real terminal does with a timeout.
		return 0, "", Partial
	}

	switch buf[1] {
	case '[':
		if frame == nil {
			frame = FrameCSI
		}
		body, final, n, r := frame(buf)
		if r != Full {
			return 0, "", r
		}
		tok, ok := LegacyCSIToken(body, final)
		if !ok {
			return 0, "", NoMatch
		}
		return n, tok, Full
	case 'O':
		if len(buf) < 3 {
			return 0, "", Partial
		}
		d, known := KeyBySS3[buf[2]]
		if !known {
			return 0, "", NoMatch
		}
		return 3, d.Name, Full
	}

	// ESC followed by another control-string introducer is not a key at all:
	// it opens an OSC, DCS, APC, PM or SOS string. Declining here is what
	// stops a terminal reply such as the kitty graphics APC being shredded
	// into Alt+_ plus text plus Alt+backslash.
	//
	// ESC \ is in the list for the mirror-image reason: it is the string
	// terminator. A well formed reply is consumed whole by the framer and
	// never reaches this code, but a reply closed by the 8-bit terminator
	// leaves the 7-bit one stranded, and reading that as Alt+backslash is the
	// reported defect in miniature. The trade is the documented one: a user
	// who really presses Alt+backslash gets a Raw line, which replays exactly
	// and only costs legibility, whereas guessing "key" corrupts a reply
	// silently.
	switch buf[1] {
	case ']', 'P', '_', '^', 'X', 'N', '\\':
		return 0, "", NoMatch
	}

	// ESC followed by a printable rune is the meta encoding of Alt. ESC
	// followed by a control byte is Alt applied to that control key.
	if buf[1] == 0x7f {
		return 2, "Alt+Backspace", Full
	}
	if buf[1] == 0x1b {
		// ESC ESC is Alt held on the Esc key, which the kitty protocol
		// sends as CSI 27;3 u. Read as Ctrl+[ it would leave as CSI 91;7 u.
		return 2, "Alt+Esc", Full
	}
	if buf[1] < 0x20 {
		return 2, AddAlt(C0Token(buf[1])), Full
	}
	if !utf8.FullRune(buf[1:]) {
		return 0, "", Partial
	}
	r, size := utf8.DecodeRune(buf[1:])
	if r == utf8.RuneError && size == 1 {
		return 0, "", NoMatch
	}
	return 1 + size, "Alt+" + RuneToken(r), Full
}

// LegacyCSIToken names the cursor or function key that a legacy CSI sequence
// encodes, from its parameter body and final byte. It declines anything with a
// private-parameter prefix or an unknown final byte, so mouse reports, focus
// events, kitty keys and terminal replies fall to the protocols that own them.
func LegacyCSIToken(body []byte, final byte) (string, bool) {
	// Only digits and ';' are legacy key parameters. A private prefix such
	// as '?' or '<', or a sub-parameter ':', means the sequence belongs to
	// another protocol.
	for _, b := range body {
		if (b < 0x30 || b > 0x39) && b != ';' {
			return "", false
		}
	}

	params := splitParams(string(body))

	switch final {
	case '~':
		if len(params) == 0 {
			return "", false
		}
		d, known := KeyByTilde[params[0]]
		if !known {
			return "", false
		}
		mask, okMod := modParam(params, 1)
		if !okMod {
			return "", false
		}
		return ModString(mask) + d.Name, true

	default:
		d, known := KeyByLetter[final]
		if !known {
			ss3, isFn := KeyBySS3[final]
			if !isFn || ss3.Form != FormSS3 {
				return "", false
			}
			// A function key in the SS3 family reaches the CSI form only
			// when it carries an explicit, non-default modifier, because
			// its unmodified spelling is SS3. Without this rule CSI R
			// would decode as F3, when in fact it is the cursor position
			// report; the same collision exists for CSI S. The modifier
			// must be a real one: an omitted, empty or "no modifiers"
			// parameter is a default, so neither CSI 1;R nor CSI 1;1R is
			// F3, and the latter is a cursor report for row 1.
			if len(params) < 2 || params[1] < 2 {
				return "", false
			}
			d = ss3
		}
		// The letter form takes a leading dummy parameter of 1.
		if len(params) > 0 && params[0] != 1 {
			return "", false
		}
		mask, okMod := modParam(params, 1)
		if !okMod {
			return "", false
		}
		return ModString(mask) + d.Name, true
	}
}

// EncodeLegacy renders one key token in the legacy encoding. appCursor is
// DECCKM: the unmodified cursor keys then use their SS3 form. It is the single
// place that turns a token into legacy bytes, so every caller agrees.
func EncodeLegacy(tok string, appCursor bool) ([]byte, bool) {
	mask, base, ok := SplitToken(tok)
	if !ok {
		return nil, false
	}

	if d, known := KeyByName[base]; known {
		return encodeNamedKey(d, mask, appCursor), true
	}

	// Keys that are a control byte rather than a table entry.
	switch base {
	case "Enter", "Return":
		return withAlt(mask, []byte{'\r'})
	case "Tab":
		return withAlt(mask, []byte{'\t'})
	case "Esc", "Escape":
		return withAlt(mask, []byte{0x1b})
	case "Backspace":
		return withAlt(mask, []byte{0x7f})
	case "Space":
		if mask&^ModAlt == 0 {
			return withAlt(mask, []byte{' '})
		}
	}

	// A bare rune, possibly with Ctrl and Alt applied the legacy way. A
	// control character is not a legal key token: it has a name such as
	// Ctrl+p or Tab, and accepting the raw byte would give the same key two
	// spellings that encode differently under other protocols.
	r, isRune := SingleRune(base)
	if !isRune || r < 0x20 || r == 0x7f {
		return nil, false
	}
	switch {
	case mask&ModCtrl != 0:
		if mask&^(ModCtrl|ModAlt) != 0 || !HasLegacyCtrl(r) {
			return nil, false
		}
		return withAlt(mask&ModAlt, []byte{ctrlByte(r)})
	case mask&ModShift != 0:
		if mask&^(ModShift|ModAlt) != 0 {
			return nil, false
		}
		up := strings.ToUpper(base)
		if up == base {
			return nil, false
		}
		return withAlt(mask&ModAlt, []byte(up))
	case mask&^ModAlt == 0:
		return withAlt(mask, []byte(base))
	}
	return nil, false
}

// withAlt applies the ESC prefix for Alt and rejects any other leftover
// modifier, which has no legacy encoding on a bare rune.
func withAlt(mask int, b []byte) ([]byte, bool) {
	if mask&^ModAlt != 0 {
		return nil, false
	}
	if mask&ModAlt != 0 {
		return append([]byte{0x1b}, b...), true
	}
	return b, true
}

// encodeNamedKey renders a table key with its modifier parameter.
func encodeNamedKey(d KeyDef, mask int, appCursor bool) []byte {
	param := mask + 1

	switch d.Form {
	case FormTilde:
		if mask == 0 {
			return []byte("\x1b[" + strconv.Itoa(d.Num) + "~")
		}
		return []byte("\x1b[" + strconv.Itoa(d.Num) + ";" + strconv.Itoa(param) + "~")

	case FormSS3:
		if mask == 0 {
			return []byte{0x1b, 'O', d.Final}
		}
		// SS3 cannot carry a parameter, so a modified function key uses
		// the CSI form, which is what terminals emit.
		return []byte("\x1b[1;" + strconv.Itoa(param) + string(d.Final))

	default: // FormLetter
		if mask == 0 {
			if d.AppCursor && appCursor {
				return []byte{0x1b, 'O', d.Final}
			}
			return []byte{0x1b, '[', d.Final}
		}
		return []byte("\x1b[1;" + strconv.Itoa(param) + string(d.Final))
	}
}

// C0Token names a C0 control byte as a tape token. The mapping is the exact
// inverse of EncodeLegacy, so a decoded token replays byte for byte.
func C0Token(b byte) string {
	switch b {
	case '\t':
		return "Tab"
	case '\r':
		return "Enter"
	case 0x7f:
		return "Backspace"
	case 0:
		return "Ctrl+@"
	}
	if b >= 1 && b <= 26 {
		return "Ctrl+" + string(rune('a'+b-1))
	}
	// 0x1c-0x1f are Ctrl with the punctuation whose low five bits match.
	return "Ctrl+" + string(rune(b|0x40))
}

// ctrlByte is the inverse of the Ctrl half of C0Token.
func ctrlByte(r rune) byte {
	if r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	return byte(r) & 0x1f
}

// splitParams parses a semicolon-separated CSI parameter list. An empty field
// is the default, which this grammar always spells as 0 and callers interpret.
func splitParams(s string) []int {
	if s == "" {
		return nil
	}
	fields := strings.Split(s, ";")
	out := make([]int, len(fields))
	for i, f := range fields {
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil
		}
		out[i] = v
	}
	return out
}

// modParam reads the modifier parameter at index i, returning the bitmask. The
// wire value is the mask plus one, so 1 means no modifiers and 0 means the
// parameter was omitted.
func modParam(params []int, i int) (int, bool) {
	if i >= len(params) || params[i] == 0 {
		return 0, true
	}
	if params[i] < 1 {
		return 0, false
	}
	return params[i] - 1, true
}

// AddAlt adds the Alt modifier to an already-spelled token, keeping the
// canonical modifier order. Concatenating "Alt+" would spell Ctrl+Alt+a as
// "Alt+Ctrl+a", which decodes to the same key but makes the tape inconsistent
// about how a chord is written.
func AddAlt(tok string) string {
	mask, base, ok := SplitToken(tok)
	if !ok {
		return "Alt+" + tok
	}
	return ModString(mask|ModAlt) + base
}

// RuneToken spells a rune as a key token. A space has to be written as the name
// Space, because whitespace separates tokens on a tape line and a literal space
// inside a token could not be read back.
func RuneToken(r rune) string {
	if r == ' ' {
		return "Space"
	}
	return string(r)
}

// HasLegacyCtrl reports whether Ctrl combined with this rune has a legacy
// encoding that decodes back to the same key. Only the letters and the six
// punctuation marks whose low five bits are unique do; Ctrl and '+' would
// produce 0x0b, which reads back as Ctrl+k, so it has no legacy spelling and
// belongs to modifyOtherKeys or kitty instead.
func HasLegacyCtrl(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
		return true
	}
	switch r {
	case '@', '[', '\\', ']', '^', '_':
		return true
	}
	return false
}
