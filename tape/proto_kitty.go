package tape

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func init() { Register(kittyKeys{}) }

// kittyKeys decodes the kitty keyboard protocol, the CSI u encoding a program
// opts into with progressive enhancement flags. It reports what the legacy
// encoding cannot: key release and auto-repeat as well as press, the shifted
// and base-layout forms of the physical key, and the text a keypress would
// insert, which for a dead key or an input method is not the key itself.
//
// The wire form is
//
//	CSI code[:shifted[:base]] [; mods[:event]] [; text[:text...]] final
//
// where final is 'u' for keys identified by codepoint, or the legacy '~' and
// letter finals for keys that already had one. This decoder accepts both,
// because a terminal with kitty enabled keeps using the legacy final for those
// keys and only adds the extra parameters.
//
// It is Canonical: default parameters that were omitted are not restored on
// re-encode, and a key that carries no information beyond the chord is spelled
// as the plain chord. Both re-encode to bytes that decode to the same command.
type kittyKeys struct{}

func (kittyKeys) Name() string       { return "kitty-keys" }
func (kittyKeys) Priority() int      { return 20 }
func (kittyKeys) Fidelity() Fidelity { return Canonical }

// Keyboard reports true: this protocol decodes keystrokes.
func (kittyKeys) Keyboard() bool { return true }

// Kitty event types.
const (
	kittyPress   = 1
	kittyRepeat  = 2
	kittyRelease = 3
)

func (p kittyKeys) Decode(buf []byte, m Modes) (int, []Command, Result) {
	body, final, n, r := csiFrame(buf)
	if r != Full {
		return 0, nil, r
	}

	params, ok := parseCSIParams(string(body))
	if !ok || len(params) == 0 {
		return 0, nil, NoMatch
	}

	// modifyOtherKeys owns the CSI 27 ; mod ; code ~ form.
	if final == '~' && params.at(0, 0, -1) == 27 && len(params) == 3 {
		return 0, nil, NoMatch
	}

	base, ok := p.baseKey(params, final)
	if !ok {
		return 0, nil, NoMatch
	}

	mask := params.at(1, 0, 1) - 1
	if mask < 0 {
		return 0, nil, NoMatch
	}
	event := params.at(1, 1, kittyPress)

	var attrs KeyAttrs
	switch event {
	case kittyPress:
	case kittyRepeat:
		attrs.Event = KeyRepeat
	case kittyRelease:
		attrs.Event = KeyRelease
	default:
		return 0, nil, NoMatch
	}

	if params.has(0, 1) {
		r := rune(params.at(0, 1, 0))
		if !validRune(r) {
			return 0, nil, NoMatch
		}
		attrs.Shifted = string(r)
	}
	if params.has(0, 2) {
		r := rune(params.at(0, 2, 0))
		if !validRune(r) {
			return 0, nil, NoMatch
		}
		attrs.Base = string(r)
	}
	if text := params.group(2); len(text) > 0 {
		var b strings.Builder
		for _, cp := range text {
			if cp < 0 {
				return 0, nil, NoMatch
			}
			r := rune(cp)
			if !validRune(r) {
				return 0, nil, NoMatch
			}
			b.WriteRune(r)
		}
		attrs.Text = b.String()
	}

	// A key with no extra information is spelled as the plain chord, which
	// keeps an ordinary recording readable even under kitty.
	cmd := Command{Kind: KindKey, Keys: []string{modString(mask) + base}, KeyAttrs: attrs}
	cmd.Modes.KittyFlags = max(m.KittyFlags, 1)
	return n, []Command{cmd}, Full
}

// baseKey names the key a kitty report identifies, from the codepoint for the
// 'u' final or from the legacy tables for the others.
func (p kittyKeys) baseKey(params csiParams, final byte) (string, bool) {
	code := params.at(0, 0, -1)
	if code < 0 {
		return "", false
	}

	switch final {
	case 'u':
		if d, known := keyByKitty[code]; known {
			return d.name, true
		}
		switch code {
		case 27:
			return "Esc", true
		case 13:
			return "Enter", true
		case 9:
			return "Tab", true
		case 127:
			return "Backspace", true
		case 32:
			return "Space", true
		}
		// A code in the functional block that this table does not name is a
		// key from a newer revision of the protocol. Spelling it as the
		// Private Use Area character it happens to equal would be a lie, so
		// decline and let it be captured verbatim as Raw.
		if isKittyFunctionalCode(code) {
			return "", false
		}
		r := rune(code)
		if !validRune(r) || r < 0x20 {
			return "", false
		}
		return string(r), true

	case '~':
		d, known := keyByTilde[code]
		if !known {
			return "", false
		}
		return d.name, true

	default:
		if code != 1 {
			return "", false
		}
		if d, known := keyByLetter[final]; known {
			return d.name, true
		}
		// As in the legacy protocol, a function key in the SS3 family
		// reaches the CSI form only with a real modifier. Otherwise CSI R
		// would be read as F3 when it is the cursor position report, and
		// CSI 1;1R when it is a report for row 1. The leading parameter
		// must also be a bare 1: a sub-parameter there describes an
		// alternate key layout, which a key identified by its final byte
		// does not have, so its presence means this is not a key report.
		if d, known := keyBySS3[final]; known && d.form == formSS3 &&
			params.at(1, 0, 1) >= 2 && len(params.group(0)) == 1 {
			return d.name, true
		}
		return "", false
	}
}

func (p kittyKeys) Encode(c Command, m Modes) ([]byte, bool) {
	if c.Kind != KindKey {
		return nil, false
	}
	// Claim a command only when kitty is in force, so a recording made
	// without it keeps the legacy spelling. Attributes are proof on their
	// own: no other protocol can express a release or associated text.
	if m.KittyFlags == 0 && c.Modes.KittyFlags == 0 && !c.KeyAttrs.set() {
		return nil, false
	}
	// A Key line carrying attributes names exactly one key, so attributes
	// are never ambiguous about which key they qualify.
	if c.KeyAttrs.set() && len(c.Keys) != 1 {
		return nil, false
	}

	var out []byte
	for _, tok := range c.Keys {
		b, ok := encodeKitty(tok, c.KeyAttrs)
		if !ok {
			return nil, false
		}
		out = append(out, b...)
	}
	return out, true
}

// encodeKitty renders one chord in the CSI u grammar.
func encodeKitty(tok string, a KeyAttrs) ([]byte, bool) {
	mask, base, ok := splitToken(tok)
	if !ok {
		return nil, false
	}

	code, final, ok := kittyCodeFor(base)
	if !ok {
		return nil, false
	}

	var b strings.Builder
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(code))

	// Sub-parameters of the key code: the shifted and base layouts. Base
	// requires a placeholder for shifted when only base is present, since
	// position is what identifies them.
	switch {
	case a.Shifted != "" && a.Base != "":
		b.WriteByte(':')
		b.WriteString(runeParam(a.Shifted))
		b.WriteByte(':')
		b.WriteString(runeParam(a.Base))
	case a.Shifted != "":
		b.WriteByte(':')
		b.WriteString(runeParam(a.Shifted))
	case a.Base != "":
		b.WriteString("::")
		b.WriteString(runeParam(a.Base))
	}

	event := 0
	switch a.Event {
	case KeyRepeat:
		event = kittyRepeat
	case KeyRelease:
		event = kittyRelease
	}

	needMods := mask != 0 || event != 0
	if needMods || a.Text != "" {
		b.WriteByte(';')
		if needMods {
			b.WriteString(strconv.Itoa(mask + 1))
			if event != 0 {
				b.WriteByte(':')
				b.WriteString(strconv.Itoa(event))
			}
		}
	}
	if a.Text != "" {
		b.WriteByte(';')
		for i, r := range a.Text {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(strconv.Itoa(int(r)))
		}
	}
	b.WriteByte(final)
	return []byte(b.String()), true
}

// kittyCodeFor maps a tape key name to its kitty key code and final byte.
func kittyCodeFor(base string) (code int, final byte, ok bool) {
	switch base {
	case "Esc", "Escape":
		return 27, 'u', true
	case "Enter", "Return":
		return 13, 'u', true
	case "Tab":
		return 9, 'u', true
	case "Backspace":
		return 127, 'u', true
	case "Space":
		return 32, 'u', true
	}
	if d, known := keyByName[base]; known {
		if d.kittyCode != 0 {
			return d.kittyCode, 'u', true
		}
	}
	r, single := singleRune(base)
	if !single || r < 0x20 {
		return 0, 0, false
	}
	// Kitty numbers its functional keys inside the Unicode Private Use Area,
	// so a literal character from that block cannot be spelled as itself: the
	// reader would take it for a function key. Decline and let the legacy
	// encoding carry it, which it does exactly.
	if isKittyFunctionalCode(int(r)) {
		return 0, 0, false
	}
	return int(r), 'u', true
}

// Kitty's functional keys occupy this Private Use Area block. The range is part
// of the protocol rather than a guess, and it is the reason a PUA character is
// not encodable as a kitty key code.
const (
	kittyFunctionalLo = 57344
	kittyFunctionalHi = 57454
)

func isKittyFunctionalCode(code int) bool {
	return code >= kittyFunctionalLo && code <= kittyFunctionalHi
}

// runeParam renders the first rune of s as a decimal codepoint parameter.
func runeParam(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	return strconv.Itoa(int(r))
}

// validRune rejects surrogates and out-of-range codepoints, which cannot be
// carried in a Go string and so could not survive the round trip.
func validRune(r rune) bool {
	return r > 0 && r <= 0x10ffff && !(r >= 0xd800 && r <= 0xdfff)
}
