package keyenc

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kitty keyboard progressive enhancement flags, as a program sets them with
// CSI > flags u or CSI = flags ; mode u.
const (
	// KittyDisambiguate makes Esc and keys with Ctrl or Alt use CSI u.
	KittyDisambiguate = 1
	// KittyEvents asks for repeat and release events as well as presses.
	KittyEvents = 2
	// KittyAlternates asks for the shifted form of a key with Shift held.
	KittyAlternates = 4
	// KittyAllKeys sends every key as an escape code, text keys and Enter,
	// Tab and Backspace included.
	KittyAllKeys = 8
	// KittyText adds the text a key types to its escape code. It has an
	// effect only with KittyAllKeys.
	KittyText = 16
)

// Kitty event types on the wire.
const (
	KittyPress   = 1
	KittyRepeat  = 2
	KittyRelease = 3
)

// Attrs is what the kitty protocol reports about a key beyond the chord: the
// event type, the shifted and base layouts of the physical key, and the text
// the key types. The zero value is a plain press.
type Attrs struct {
	// Event is KittyRepeat or KittyRelease, or zero for a press.
	Event   int
	Shifted string
	Base    string
	Text    string
}

// EncodeKitty renders one key token the way a terminal speaking the kitty
// keyboard protocol sends it, under the given progressive enhancement flags.
// appCursor is DECCKM, which still applies to a key kitty sends in its legacy
// form.
//
// The rules are the protocol's. Esc and any key with Ctrl or Alt use CSI u.
// Enter, Tab, Backspace and the text keys stay in their legacy form unless
// KittyAllKeys is set. The cursor keys, Home, End, Insert, Delete, PageUp,
// PageDown and F1 to F12 keep their legacy CSI form, with the modifier as a
// parameter, and F3 is CSI 13 ~.
//
// Explicit attributes in a are written as they are, whatever the flags: they
// come from a recording, which shows what the terminal really sent.
func EncodeKitty(tok string, a Attrs, flags int, appCursor bool) ([]byte, bool) {
	mask, base, ok := SplitToken(tok)
	if !ok {
		return nil, false
	}
	explicit := a != (Attrs{})

	if d, known := KeyByName[base]; known {
		if a.Shifted != "" || a.Base != "" || a.Text != "" {
			// The legacy form has no room for these. Only the CSI u form
			// with the key's code can carry them.
			if d.KittyCode == 0 {
				return nil, false
			}
			return kittyCSIu(d.KittyCode, mask, a), true
		}
		return kittyNamed(d, mask, a.Event, flags, appCursor), true
	}

	code, isText, ok := kittyCodeFor(base)
	if !ok {
		return nil, false
	}
	if !explicit && flags&KittyAllKeys == 0 {
		switch {
		case base == "Esc" || base == "Escape":
			// Disambiguated: a lone ESC byte is never sent.
		case mask == 0 && !isText:
			// Enter, Tab and Backspace stay legacy, so a shell stays
			// usable after a program that set the mode crashes.
			return EncodeLegacy(tok, appCursor)
		case isText && mask&^ModShift == 0:
			// Typing text sends the text.
			if b, ok := EncodeLegacy(tok, appCursor); ok {
				return b, true
			}
		}
	}
	if !explicit {
		r, single := SingleRune(base)
		if flags&KittyAlternates != 0 && mask&ModShift != 0 && single {
			if up := unicode.ToUpper(r); up != r {
				a.Shifted = string(up)
			}
		}
		if flags&KittyAllKeys != 0 && flags&KittyText != 0 && isText && mask&^ModShift == 0 {
			switch {
			case base == "Space":
				a.Text = " "
			case single && mask&ModShift != 0:
				a.Text = string(unicode.ToUpper(r))
			case single:
				a.Text = base
			}
		}
	}
	return kittyCSIu(code, mask, a), true
}

// kittyNamed renders a named key in its legacy form, as the kitty protocol
// keeps it, with the modifier and an event type as parameters.
func kittyNamed(d KeyDef, mask, event, flags int, appCursor bool) []byte {
	if mask == 0 && event == 0 && flags&KittyAllKeys == 0 {
		return encodeNamedKey(d, 0, appCursor)
	}
	num, final := "1", d.Final
	switch {
	case d.Form == FormTilde:
		num, final = strconv.Itoa(d.Num), '~'
	case d.Form == FormSS3 && d.Final == 'R':
		// CSI R is the cursor position report, so kitty sends F3 as CSI 13 ~.
		num, final = "13", '~'
	}
	var b strings.Builder
	b.WriteString("\x1b[")
	if mask != 0 || event != 0 {
		b.WriteString(num)
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(mask + 1))
		if event != 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(event))
		}
	} else if final == '~' {
		b.WriteString(num)
	}
	b.WriteByte(final)
	return []byte(b.String())
}

// kittyCSIu renders one key in the CSI u grammar:
//
//	CSI code[:shifted[:base]] [; mods[:event]] [; text[:text...]] u
func kittyCSIu(code, mask int, a Attrs) []byte {
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

	needMods := mask != 0 || a.Event != 0
	if needMods || a.Text != "" {
		b.WriteByte(';')
		if needMods {
			b.WriteString(strconv.Itoa(mask + 1))
			if a.Event != 0 {
				b.WriteByte(':')
				b.WriteString(strconv.Itoa(a.Event))
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
	b.WriteByte('u')
	return []byte(b.String())
}

// kittyCodeFor maps a key that is not a named table key to its CSI u code, and
// reports whether it types text.
func kittyCodeFor(base string) (code int, isText, ok bool) {
	switch base {
	case "Esc", "Escape":
		return 27, false, true
	case "Enter", "Return":
		return 13, false, true
	case "Tab":
		return 9, false, true
	case "Backspace":
		return 127, false, true
	case "Space":
		return 32, true, true
	}
	r, single := SingleRune(base)
	// A control character is not a legal key token, which is the same rule the
	// legacy encoder states: it has a name, such as Tab or Backspace, and
	// accepting the raw byte as well would give one key two spellings that
	// encode to different bytes. DEL is the case that showed the two encoders
	// disagreeing: legacy rejected "\x7f" and kitty accepted it, so the token
	// resolved through the permissive fallback and a tape line "Key \x7f" sent
	// "\x1b[127u" to a program that had never negotiated kitty.
	if !single || r < 0x20 || r == 0x7f {
		return 0, false, false
	}
	// Kitty numbers its functional keys inside the Unicode Private Use Area,
	// so a literal character from that block cannot be spelled as itself: the
	// reader would take it for a function key. Decline and let the legacy
	// encoding carry it, which it does exactly.
	if IsKittyFunctionalCode(int(r)) {
		return 0, false, false
	}
	return int(r), true, true
}

// Kitty's functional keys occupy this Private Use Area block. The range is part
// of the protocol rather than a guess, and it is the reason a PUA character is
// not encodable as a kitty key code.
const (
	kittyFunctionalLo = 57344
	kittyFunctionalHi = 57454
)

// IsKittyFunctionalCode reports whether code is in kitty's functional key
// block.
func IsKittyFunctionalCode(code int) bool {
	return code >= kittyFunctionalLo && code <= kittyFunctionalHi
}

// runeParam renders the first rune of s as a decimal codepoint parameter.
func runeParam(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	return strconv.Itoa(int(r))
}

// ValidRune rejects surrogates and out-of-range codepoints, which cannot be
// carried in a Go string and so could not survive the round trip.
func ValidRune(r rune) bool {
	return r > 0 && r <= 0x10ffff && !(r >= 0xd800 && r <= 0xdfff)
}
