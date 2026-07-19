package tape

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func init() { Register(legacyKeys{}) }

// legacyKeys decodes the keyboard encoding every terminal speaks without any
// negotiation: C0 control bytes, the ESC-prefixed meta encoding of Alt, and the
// CSI/SS3 cursor and function keys with their xterm modifier parameter.
//
// It is Canonical rather than Exact because the wire format has redundant
// spellings and the tape records the key rather than the spelling. The
// normalizations are:
//
//   - an omitted default parameter is restored, so CSI A and CSI 1 A both
//     decode to Up and re-encode as CSI A;
//   - a modified SS3 key is promoted to its CSI form, because SS3 has nowhere
//     to put a parameter, which is what real terminals do too;
//
// Each of these produces bytes that decode back to the same command, which is
// the Canonical contract and is what the round-trip fuzz target checks.
type legacyKeys struct{}

func (legacyKeys) Name() string       { return "legacy-keys" }
func (legacyKeys) Priority() int      { return 0 }
func (legacyKeys) Fidelity() Fidelity { return Canonical }

// Keyboard reports true: this protocol decodes keystrokes.
func (legacyKeys) Keyboard() bool { return true }

func (p legacyKeys) Decode(buf []byte, m Modes) (int, []Command, Result) {
	if len(buf) == 0 {
		return 0, nil, NoMatch
	}

	// C0 controls and DEL are keys in their own right.
	if buf[0] < 0x20 && buf[0] != 0x1b || buf[0] == 0x7f {
		return 1, keyCmd(c0Token(buf[0])), Full
	}
	if buf[0] != 0x1b {
		return 0, nil, NoMatch
	}
	if len(buf) == 1 {
		// A lone ESC is the Esc key. Treating it as an incomplete
		// sequence would make Esc impossible to record.
		return 1, keyCmd("Esc"), Full
	}

	switch buf[1] {
	case '[':
		return p.decodeCSI(buf, m)
	case 'O':
		return p.decodeSS3(buf)
	}

	// ESC followed by another control-string introducer is not a key at all:
	// it opens an OSC, DCS, APC, PM or SOS string. Declining here is what
	// stops a terminal reply such as the kitty graphics APC being shredded
	// into Alt+_ plus text plus Alt+backslash.
	switch buf[1] {
	case ']', 'P', '_', '^', 'X', 'N':
		return 0, nil, NoMatch
	}

	// ESC followed by a printable rune is the meta encoding of Alt. ESC
	// followed by a control byte is Alt applied to that control key.
	if buf[1] == 0x7f {
		return 2, keyCmd("Alt+Backspace"), Full
	}
	if buf[1] < 0x20 {
		return 2, keyCmd(addAlt(c0Token(buf[1]))), Full
	}
	if !utf8.FullRune(buf[1:]) {
		return 0, nil, Partial
	}
	r, size := utf8.DecodeRune(buf[1:])
	if r == utf8.RuneError && size == 1 {
		return 0, nil, NoMatch
	}
	return 1 + size, keyCmd("Alt+" + runeToken(r)), Full
}

// decodeCSI handles the CSI cursor and function keys. It deliberately declines
// anything with a private-parameter prefix or an unknown final byte so mouse
// reports, focus events, kitty keys and terminal replies fall to the protocols
// that own them.
func (p legacyKeys) decodeCSI(buf []byte, m Modes) (int, []Command, Result) {
	body, final, n, r := csiFrame(buf)
	if r != Full {
		return 0, nil, r
	}

	// Only digits and ';' are legacy key parameters. A private prefix such
	// as '?' or '<', or a sub-parameter ':', means the sequence belongs to
	// another protocol.
	for _, b := range body {
		if (b < 0x30 || b > 0x39) && b != ';' {
			return 0, nil, NoMatch
		}
	}

	params := splitParams(string(body))

	switch final {
	case '~':
		if len(params) == 0 {
			return 0, nil, NoMatch
		}
		d, known := keyByTilde[params[0]]
		if !known {
			return 0, nil, NoMatch
		}
		mask, okMod := modParam(params, 1)
		if !okMod {
			return 0, nil, NoMatch
		}
		return n, keyCmd(modString(mask) + d.name), Full

	default:
		d, known := keyByLetter[final]
		if !known {
			ss3, isFn := keyBySS3[final]
			if !isFn || ss3.form != formSS3 {
				return 0, nil, NoMatch
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
				return 0, nil, NoMatch
			}
			d = ss3
		}
		// The letter form takes a leading dummy parameter of 1.
		if len(params) > 0 && params[0] != 1 {
			return 0, nil, NoMatch
		}
		mask, okMod := modParam(params, 1)
		if !okMod {
			return 0, nil, NoMatch
		}
		return n, keyCmd(modString(mask) + d.name), Full
	}
}

// decodeSS3 handles SS3 function and application-cursor keys.
func (p legacyKeys) decodeSS3(buf []byte) (int, []Command, Result) {
	if len(buf) < 3 {
		return 0, nil, Partial
	}
	d, known := keyBySS3[buf[2]]
	if !known {
		return 0, nil, NoMatch
	}
	return 3, keyCmd(d.name), Full
}

// Encode renders a Key command back to bytes. Multi-token Key lines encode as
// the concatenation of their tokens, which is exactly what the player sends.
func (p legacyKeys) Encode(c Command, m Modes) ([]byte, bool) {
	if c.Kind != KindKey || c.KeyAttrs.set() {
		return nil, false
	}
	var out []byte
	for _, tok := range c.Keys {
		b, ok := encodeKeyToken(tok, m)
		if !ok {
			return nil, false
		}
		out = append(out, b...)
	}
	return out, true
}

// encodeKeyToken renders one chord. It is the single place that turns a tape
// token into bytes, so ResolveKey and the round-trip property cannot disagree.
func encodeKeyToken(tok string, m Modes) ([]byte, bool) {
	mask, base, ok := splitToken(tok)
	if !ok {
		return nil, false
	}

	if d, known := keyByName[base]; known {
		return encodeNamedKey(d, mask, m), true
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
		if mask&^modAlt == 0 {
			return withAlt(mask, []byte{' '})
		}
	}

	// A bare rune, possibly with Ctrl and Alt applied the legacy way. A
	// control character is not a legal key token: it has a name such as
	// Ctrl+p or Tab, and accepting the raw byte would give the same key two
	// spellings that encode differently under other protocols.
	r, isRune := singleRune(base)
	if !isRune || r < 0x20 || r == 0x7f {
		return nil, false
	}
	switch {
	case mask&modCtrl != 0:
		if mask&^(modCtrl|modAlt) != 0 || !hasLegacyCtrl(r) {
			return nil, false
		}
		return withAlt(mask&modAlt, []byte{ctrlByte(r)})
	case mask&modShift != 0:
		if mask&^(modShift|modAlt) != 0 {
			return nil, false
		}
		up := strings.ToUpper(base)
		if up == base {
			return nil, false
		}
		return withAlt(mask&modAlt, []byte(up))
	case mask&^modAlt == 0:
		return withAlt(mask, []byte(base))
	}
	return nil, false
}

// withAlt applies the ESC prefix for Alt and rejects any other leftover
// modifier, which has no legacy encoding on a bare rune.
func withAlt(mask int, b []byte) ([]byte, bool) {
	if mask&^modAlt != 0 {
		return nil, false
	}
	if mask&modAlt != 0 {
		return append([]byte{0x1b}, b...), true
	}
	return b, true
}

// encodeNamedKey renders a table key with its modifier parameter.
func encodeNamedKey(d keyDef, mask int, m Modes) []byte {
	param := mask + 1

	switch d.form {
	case formTilde:
		if mask == 0 {
			return []byte("\x1b[" + strconv.Itoa(d.num) + "~")
		}
		return []byte("\x1b[" + strconv.Itoa(d.num) + ";" + strconv.Itoa(param) + "~")

	case formSS3:
		if mask == 0 {
			return []byte{0x1b, 'O', d.final}
		}
		// SS3 cannot carry a parameter, so a modified function key uses
		// the CSI form, which is what terminals emit.
		return []byte("\x1b[1;" + strconv.Itoa(param) + string(d.final))

	default: // formLetter
		if mask == 0 {
			if d.appCursor && m.AppCursor {
				return []byte{0x1b, 'O', d.final}
			}
			return []byte{0x1b, '[', d.final}
		}
		return []byte("\x1b[1;" + strconv.Itoa(param) + string(d.final))
	}
}

// c0Token names a C0 control byte as a tape token. The mapping is the exact
// inverse of encodeKeyToken, so a decoded token replays byte for byte.
func c0Token(b byte) string {
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

// ctrlByte is the inverse of the Ctrl half of c0Token.
func ctrlByte(r rune) byte {
	if r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	return byte(r) & 0x1f
}

// keyCmd is the one-token Key command shorthand the decoders return.
func keyCmd(tok string) []Command {
	return []Command{{Kind: KindKey, Keys: []string{tok}}}
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

// addAlt adds the Alt modifier to an already-spelled token, keeping the
// canonical modifier order. Concatenating "Alt+" would spell Ctrl+Alt+a as
// "Alt+Ctrl+a", which decodes to the same key but makes the tape inconsistent
// about how a chord is written.
func addAlt(tok string) string {
	mask, base, ok := splitToken(tok)
	if !ok {
		return "Alt+" + tok
	}
	return modString(mask|modAlt) + base
}

// runeToken spells a rune as a key token. A space has to be written as the name
// Space, because whitespace separates tokens on a tape line and a literal space
// inside a token could not be read back.
func runeToken(r rune) string {
	if r == ' ' {
		return "Space"
	}
	return string(r)
}

// hasLegacyCtrl reports whether Ctrl combined with this rune has a legacy
// encoding that decodes back to the same key. Only the letters and the six
// punctuation marks whose low five bits are unique do; Ctrl and '+' would
// produce 0x0b, which reads back as Ctrl+k, so it has no legacy spelling and
// belongs to modifyOtherKeys or kitty instead.
func hasLegacyCtrl(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
		return true
	}
	switch r {
	case '@', '[', '\\', ']', '^', '_':
		return true
	}
	return false
}
