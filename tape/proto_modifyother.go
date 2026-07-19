package tape

import (
	"strconv"
	"unicode"
)

func init() { Register(modifyOtherKeys{}) }

// modifyOtherKeys decodes xterm's modifyOtherKeys encoding, CSI 27 ; mod ; code
// ~, which exists so a program can tell Ctrl+I from Tab and Ctrl+M from Enter.
// The legacy encoding collapses those onto the same control byte, which is why
// xterm added a form that reports the modifiers separately from the key.
//
// It is Canonical: the tape records the key and its modifiers, and re-encoding
// always produces the CSI 27 form even when the recording used a redundant
// spelling such as an omitted default modifier.
type modifyOtherKeys struct{}

func (modifyOtherKeys) Name() string       { return "modify-other-keys" }
func (modifyOtherKeys) Priority() int      { return 10 }
func (modifyOtherKeys) Fidelity() Fidelity { return Canonical }

// Keyboard reports true: this protocol decodes keystrokes.
func (modifyOtherKeys) Keyboard() bool { return true }

func (p modifyOtherKeys) Decode(buf []byte, m Modes) (int, []Command, Result) {
	body, final, n, r := csiFrame(buf)
	if r != Full {
		return 0, nil, r
	}
	if final != '~' {
		return 0, nil, NoMatch
	}

	params, ok := parseCSIParams(string(body))
	if !ok || len(params) != 3 || params.at(0, 0, -1) != 27 {
		return 0, nil, NoMatch
	}

	mask := params.at(1, 0, 1) - 1
	code := params.at(2, 0, -1)
	if mask < 0 || code < 0 {
		return 0, nil, NoMatch
	}

	tok, ok := chordToken(mask, rune(code))
	if !ok {
		return 0, nil, NoMatch
	}
	cmd := Command{Kind: KindKey, Keys: []string{tok}}
	cmd.Modes.ModifyOtherKeys = max(m.ModifyOtherKeys, 1)
	return n, []Command{cmd}, Full
}

func (p modifyOtherKeys) Encode(c Command, m Modes) ([]byte, bool) {
	// Only claim a command when modifyOtherKeys is actually in force, so an
	// ordinary recording keeps its ordinary spelling.
	if c.Kind != KindKey || c.KeyAttrs.set() {
		return nil, false
	}
	if m.ModifyOtherKeys == 0 && c.Modes.ModifyOtherKeys == 0 {
		return nil, false
	}
	var out []byte
	for _, tok := range c.Keys {
		mask, r, ok := tokenChord(tok)
		if !ok {
			return nil, false
		}
		out = append(out, "\x1b[27;"+strconv.Itoa(mask+1)+";"+strconv.Itoa(int(r))+"~"...)
	}
	return out, true
}

// chordToken spells a modifier mask and codepoint as a tape key token. It
// declines named keys, which have their own spelling, so modifyOtherKeys never
// competes with the legacy table for a key that already has a name.
func chordToken(mask int, r rune) (string, bool) {
	if r == 0 || r > 0x10ffff {
		return "", false
	}
	switch r {
	case '\r':
		return modString(mask) + "Enter", true
	case '\t':
		return modString(mask) + "Tab", true
	case 0x1b:
		return modString(mask) + "Esc", true
	case 0x7f:
		return modString(mask) + "Backspace", true
	case ' ':
		return modString(mask) + "Space", true
	}
	if r < 0x20 {
		return "", false
	}
	// The tape grammar spells a shifted letter with a lowercase base, because
	// Shift is what turns "a" into "A"; "Shift+A" names the shift twice and the
	// key resolver rejects it. xterm reports the shifted letter by its uppercase
	// codepoint, so the case is folded here and restored in tokenChord.
	if mask&modShift != 0 {
		lower := unicode.ToLower(r)
		if lower == r {
			// Shift on something that is not an uppercase letter has no
			// spelling this grammar can represent unambiguously. Decline it and
			// let the bytes be captured raw, which still replays exactly.
			return "", false
		}
		return modString(mask) + string(lower), true
	}
	return modString(mask) + string(r), true
}

// tokenChord is the inverse of chordToken.
func tokenChord(tok string) (int, rune, bool) {
	mask, base, ok := splitToken(tok)
	if !ok {
		return 0, 0, false
	}
	switch base {
	case "Enter", "Return":
		return mask, '\r', true
	case "Tab":
		return mask, '\t', true
	case "Esc", "Escape":
		return mask, 0x1b, true
	case "Backspace":
		return mask, 0x7f, true
	case "Space":
		return mask, ' ', true
	}
	if _, named := keyByName[base]; named {
		return 0, 0, false
	}
	// Mirror chordToken: a control character is spelled by name, never as
	// itself, so encoding one here would produce bytes this protocol then
	// refuses to decode.
	r, single := singleRune(base)
	if !single || r < 0x20 || r == 0x7f {
		return 0, 0, false
	}
	// Invert the case folding chordToken applies, so Shift+a re-encodes to the
	// uppercase codepoint xterm actually reports.
	if mask&modShift != 0 {
		upper := unicode.ToUpper(r)
		if upper == r {
			return 0, 0, false
		}
		return mask, upper, true
	}
	return mask, r, true
}
