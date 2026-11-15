package keyenc

import (
	"unicode"
	"unicode/utf8"
)

// Modes is the keyboard state a program negotiated, which decides the bytes a
// terminal sends for a key.
type Modes struct {
	// AppCursor is DECCKM (mode 1).
	AppCursor bool
	// KittyFlags is the top of the kitty keyboard flag stack, 0 when off.
	KittyFlags int
	// ModifyOtherKeys is the xterm modifyOtherKeys level, 0 when off.
	ModifyOtherKeys int
}

// Encode renders one key token the way a terminal sends it under m: in the
// kitty protocol when the program set kitty flags, as CSI 27 for a key with
// Ctrl or Alt under modifyOtherKeys level 2, and in the legacy encoding
// otherwise.
//
// Level 1 of modifyOtherKeys changes only keys that have no legacy encoding,
// and a key that reached the encoder has one, so it keeps it.
func Encode(tok string, m Modes) ([]byte, bool) {
	if m.KittyFlags != 0 {
		return EncodeKitty(tok, Attrs{}, m.KittyFlags, m.AppCursor)
	}
	if m.ModifyOtherKeys >= 2 {
		if mask, _, ok := SplitToken(tok); ok && mask&(ModCtrl|ModAlt) != 0 {
			if b, ok := EncodeModifyOtherKeys(tok); ok {
				return b, true
			}
		}
	}
	return EncodeLegacy(tok, m.AppCursor)
}

// Reencode takes keys written in the legacy encoding and returns the bytes a
// terminal sends for the same keys under m. It is how the root package sends
// a Key, whose value is legacy bytes, to a program that negotiated another
// keyboard mode.
//
// A key whose bytes would not change under m keeps its own spelling, so input
// in an unusual but valid legacy form passes through as it was written. Bytes
// that are no key pass through too. Text inside the input is sent as text,
// except under the kitty flag that reports every key, where a terminal sends
// each typed character as a key.
func Reencode(b []byte, m Modes) []byte {
	var out []byte
	for len(b) > 0 {
		n, tok, r := DecodeLegacy(b, nil)
		switch r {
		case Partial:
			if len(b) == 1 && b[0] == 0x1b {
				// A lone ESC at the end of the input is the Esc key: no
				// more bytes are coming to complete a longer key.
				n, tok, r = 1, "Esc", Full
			}
		case NoMatch:
			if b[0] == 0x1b {
				// An escape sequence that is no legacy key, such as a kitty
				// key the caller spelled out or an OSC string, is sent
				// whole. Sent a byte at a time, its text would be read as
				// typed characters below.
				n := len(b)
				if _, _, cn, cr := FrameCSI(b); cr == Full {
					n = cn
				}
				out = append(out, b[:n]...)
				b = b[n:]
				continue
			}
			if rn, size := utf8.DecodeRune(b); m.KittyFlags&KittyAllKeys != 0 && rn >= 0x20 && rn != utf8.RuneError {
				n, tok, r = size, textToken(rn), Full
			}
		}
		if r != Full {
			// Not a key: send the byte as it is.
			out = append(out, b[0])
			b = b[1:]
			continue
		}
		orig := b[:n]
		b = b[n:]
		tok = shiftedLetter(tok)
		enc, ok := Encode(tok, m)
		if !ok {
			out = append(out, orig...)
			continue
		}
		if def, _ := EncodeLegacy(tok, false); string(def) == string(enc) {
			// The modes do not change this key, so its own spelling stands.
			out = append(out, orig...)
			continue
		}
		out = append(out, enc...)
	}
	return out
}

// shiftedLetter spells a chord on an uppercase letter, such as the "Alt+C"
// that ESC C decodes to, as Shift held on the lowercase key: "Alt+Shift+c".
// That is the key a person presses, and the kitty and modifyOtherKeys
// protocols report it that way: Alt+Shift+c is CSI 99;4 u, not CSI 67;3 u.
func shiftedLetter(tok string) string {
	mask, base, ok := SplitToken(tok)
	if !ok || mask == 0 {
		return tok
	}
	r, single := SingleRune(base)
	if !single {
		return tok
	}
	lower := unicode.ToLower(r)
	if lower == r || unicode.ToUpper(lower) != r {
		return tok
	}
	return ModString(mask|ModShift) + string(lower)
}

// textToken spells a typed character as a key token: an uppercase letter is
// Shift and the lowercase key, as the kitty protocol reports it.
func textToken(r rune) string {
	if lower := unicode.ToLower(r); lower != r && unicode.ToUpper(lower) == r {
		return "Shift+" + string(lower)
	}
	return RuneToken(r)
}
