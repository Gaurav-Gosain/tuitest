package tape

import (
	"strings"

	"github.com/Gaurav-Gosain/tuitest/internal/keyenc"
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
// as the plain chord. A named key such as Up re-encodes in the legacy form the
// protocol specifies, CSI 1 ; mods A, whatever code it arrived with. All of
// these re-encode to bytes that decode to the same command.
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

	// With every key reported, kitty sends an unmodified F1, F2 or F4 as CSI
	// P, CSI Q or CSI S. Those bytes mean nothing else on the input side, but
	// they are read as keys only when kitty is in force.
	if len(body) == 0 && m.KittyFlags != 0 {
		if d, known := keyBySS3[final]; known && d.Form == formSS3 && final != 'R' {
			cmd := Command{Kind: KindKey, Keys: []string{d.Name}}
			cmd.Modes.KittyFlags = m.KittyFlags | keyenc.KittyAllKeys
			return n, []Command{cmd}, Full
		}
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
		if !keyenc.ValidRune(r) {
			return 0, nil, NoMatch
		}
		attrs.Shifted = string(r)
	}
	if params.has(0, 2) {
		r := rune(params.at(0, 2, 0))
		if !keyenc.ValidRune(r) {
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
			if !keyenc.ValidRune(r) {
				return 0, nil, NoMatch
			}
			b.WriteRune(r)
		}
		attrs.Text = b.String()
	}

	// A key with no extra information is spelled as the plain chord, which
	// keeps an ordinary recording readable even under kitty.
	cmd := Command{Kind: KindKey, Keys: []string{modString(mask) + base}, KeyAttrs: attrs}
	cmd.Modes.KittyFlags = max(m.KittyFlags, 1) | impliedKittyFlags(mask, base)
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
			return d.Name, true
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
		if keyenc.IsKittyFunctionalCode(code) {
			return "", false
		}
		r := rune(code)
		if !keyenc.ValidRune(r) || r < 0x20 {
			return "", false
		}
		return string(r), true

	case '~':
		d, known := keyByTilde[code]
		if !known {
			return "", false
		}
		return d.Name, true

	default:
		if code != 1 {
			return "", false
		}
		if d, known := keyByLetter[final]; known {
			return d.Name, true
		}
		// As in the legacy protocol, a function key in the SS3 family
		// reaches the CSI form only with a real modifier. Otherwise CSI R
		// would be read as F3 when it is the cursor position report, and
		// CSI 1;1R when it is a report for row 1. An event sub-parameter
		// counts as well: no report carries one, so CSI 1;1:2 P is F1
		// repeating. The leading parameter
		// must also be a bare 1: a sub-parameter there describes an
		// alternate key layout, which a key identified by its final byte
		// does not have, so its presence means this is not a key report.
		if d, known := keyBySS3[final]; known && d.Form == formSS3 &&
			(params.at(1, 0, 1) >= 2 || params.has(1, 1)) && len(params.group(0)) == 1 {
			return d.Name, true
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
	flags := max(m.KittyFlags, c.Modes.KittyFlags)
	if flags == 0 && !c.KeyAttrs.set() {
		return nil, false
	}
	// A Key line carrying attributes names exactly one key, so attributes
	// are never ambiguous about which key they qualify.
	if c.KeyAttrs.set() && len(c.Keys) != 1 {
		return nil, false
	}
	attrs := keyenc.Attrs{Shifted: c.KeyAttrs.Shifted, Base: c.KeyAttrs.Base, Text: c.KeyAttrs.Text}
	switch c.KeyAttrs.Event {
	case KeyRepeat:
		attrs.Event = kittyRepeat
	case KeyRelease:
		attrs.Event = kittyRelease
	}

	var out []byte
	for _, tok := range c.Keys {
		// A tape Key line for Enter or a letter names a key event, which
		// only the flag that reports every key sends as one. Without it the
		// key would go out as text and read back as a Type line.
		f := max(flags, keyenc.KittyDisambiguate)
		if mask, base, ok := splitToken(tok); ok {
			f |= impliedKittyFlags(mask, base)
		}
		b, ok := keyenc.EncodeKitty(tok, attrs, f, m.AppCursor)
		if !ok {
			return nil, false
		}
		out = append(out, b...)
	}
	return out, true
}

// impliedKittyFlags is what a decoded key shows about the flags in force. A key
// the protocol leaves in its legacy form unless every key is reported, such as
// a plain Enter or a typed letter, arrived as CSI u only because the program
// asked for every key. The encoder needs that flag to send it the same way.
func impliedKittyFlags(mask int, base string) int {
	switch base {
	case "Enter", "Return", "Tab", "Backspace":
		if mask == 0 {
			return keyenc.KittyAllKeys
		}
		return 0
	case "Space":
	default:
		if _, named := keyByName[base]; named {
			return 0
		}
		if _, ok := singleRune(base); !ok {
			return 0
		}
	}
	if mask&^modShift == 0 {
		return keyenc.KittyAllKeys
	}
	return 0
}
