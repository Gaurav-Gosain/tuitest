package keyenc

import (
	"strconv"
	"unicode"
)

// EncodeModifyOtherKeys renders one key token in xterm's modifyOtherKeys form,
// CSI 27 ; mod ; code ~. Named table keys have no such form and are declined.
func EncodeModifyOtherKeys(tok string) ([]byte, bool) {
	mask, r, ok := TokenChord(tok)
	if !ok {
		return nil, false
	}
	return []byte("\x1b[27;" + strconv.Itoa(mask+1) + ";" + strconv.Itoa(int(r)) + "~"), true
}

// ChordToken spells a modifier mask and codepoint as a tape key token. It
// declines named keys, which have their own spelling, so modifyOtherKeys never
// competes with the legacy table for a key that already has a name.
func ChordToken(mask int, r rune) (string, bool) {
	if r == 0 || r > 0x10ffff {
		return "", false
	}
	switch r {
	case '\r':
		return ModString(mask) + "Enter", true
	case '\t':
		return ModString(mask) + "Tab", true
	case 0x1b:
		return ModString(mask) + "Esc", true
	case 0x7f:
		return ModString(mask) + "Backspace", true
	case ' ':
		return ModString(mask) + "Space", true
	}
	if r < 0x20 {
		return "", false
	}
	// The tape grammar spells a shifted letter with a lowercase base, because
	// Shift is what turns "a" into "A"; "Shift+A" names the shift twice and the
	// key resolver rejects it. xterm reports the shifted letter by its uppercase
	// codepoint, so the case is folded here and restored in TokenChord.
	if mask&ModShift != 0 {
		lower := unicode.ToLower(r)
		if lower == r {
			// Shift on something that is not an uppercase letter has no
			// spelling this grammar can represent unambiguously. Decline it and
			// let the bytes be captured raw, which still replays exactly.
			return "", false
		}
		return ModString(mask) + string(lower), true
	}
	return ModString(mask) + string(r), true
}

// TokenChord is the inverse of ChordToken.
func TokenChord(tok string) (int, rune, bool) {
	mask, base, ok := SplitToken(tok)
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
	if _, named := KeyByName[base]; named {
		return 0, 0, false
	}
	// Mirror ChordToken: a control character is spelled by name, never as
	// itself, so encoding one here would produce bytes this protocol then
	// refuses to decode.
	r, single := SingleRune(base)
	if !single || r < 0x20 || r == 0x7f {
		return 0, 0, false
	}
	// Invert the case folding ChordToken applies, so Shift+a re-encodes to the
	// uppercase codepoint xterm actually reports.
	if mask&ModShift != 0 {
		upper := unicode.ToUpper(r)
		if upper == r {
			return 0, 0, false
		}
		return mask, upper, true
	}
	return mask, r, true
}
