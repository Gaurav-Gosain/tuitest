package tuitest

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Gaurav-Gosain/tuitest/internal/keyenc"
)

// Key is a named key or chord expressed as the escape sequence it sends. Using
// typed values means a mistyped key name is a compile error, not a silent
// mismatch at runtime.
type Key string

// Named keys. Values are the byte sequences a terminal sends for each key.
const (
	Enter     Key = "\r"
	Tab       Key = "\t"
	Esc       Key = "\x1b"
	Space     Key = " "
	Backspace Key = "\x7f"
	Delete    Key = "\x1b[3~"
	Up        Key = "\x1b[A"
	Down      Key = "\x1b[B"
	Right     Key = "\x1b[C"
	Left      Key = "\x1b[D"
	Home      Key = "\x1b[H"
	End       Key = "\x1b[F"
	PageUp    Key = "\x1b[5~"
	PageDown  Key = "\x1b[6~"
	Insert    Key = "\x1b[2~"

	F1  Key = "\x1bOP"
	F2  Key = "\x1bOQ"
	F3  Key = "\x1bOR"
	F4  Key = "\x1bOS"
	F5  Key = "\x1b[15~"
	F6  Key = "\x1b[17~"
	F7  Key = "\x1b[18~"
	F8  Key = "\x1b[19~"
	F9  Key = "\x1b[20~"
	F10 Key = "\x1b[21~"
	F11 Key = "\x1b[23~"
	F12 Key = "\x1b[24~"
)

// Ctrl returns the byte a terminal sends for a rune typed with Control held,
// so Ctrl('b') is 0x02. Letters are case-insensitive.
//
// It follows the xterm table rather than masking every rune to five bits, which
// is what makes the non-letter chords come out right: Ctrl('@'), Ctrl(' ') and
// Ctrl('2') are NUL, Ctrl('[') and Ctrl('3') are ESC, Ctrl('\\'), Ctrl(']'),
// Ctrl('^') and Ctrl('_') are 0x1c to 0x1f as are Ctrl('4') to Ctrl('7'),
// Ctrl('~') is 0x1e like Ctrl('^'), Ctrl('/') is 0x1f like Ctrl('_'), and
// Ctrl('?') and Ctrl('8') are DEL. A rune with no control encoding, such as a
// digit other than 2 to 8 or anything outside ASCII, is sent unchanged, since
// that is what a terminal without the kitty keyboard protocol sends for it.
func Ctrl(r rune) Key {
	switch {
	case r >= 'a' && r <= 'z':
		return Key([]byte{byte(r - 'a' + 1)})
	case r >= '@' && r <= '_': // @, A-Z, [, \, ], ^, _
		return Key([]byte{byte(r) & 0x1f})
	case r == ' ' || r == '2':
		return Key([]byte{0})
	case r >= '3' && r <= '7':
		return Key([]byte{byte(r-'3') + 0x1b})
	case r == '?' || r == '8':
		return Key([]byte{0x7f})
	case r == '/':
		return Key([]byte{0x1f})
	case r == '~':
		return Key([]byte{0x1e})
	default:
		return Key(string(r))
	}
}

// Alt returns k with Alt held. It accepts the same items as SendKeys.
//
// A named key with a CSI or SS3 form (the cursor keys, Home, End, Insert,
// Delete, PageUp, PageDown and F1 to F12) gets the xterm modifier form, the
// way xterm sends it: Alt(Up) is CSI 1;3 A, not ESC followed by CSI A. Any
// other item is prefixed with ESC, the conventional meta encoding, so
// Alt('x') is ESC x.
//
// An item of an unsupported type yields a bare Esc, since Alt has no way to
// report an error. Pass a string, rune, Key, or slice of those and that cannot
// happen.
func Alt(k any) Key {
	if key, ok := k.(Key); ok {
		if m, ok := withMods(key, ModAlt); ok {
			return m
		}
	}
	s, err := keyString(k, keyenc.Modes{})
	if err != nil {
		return Esc
	}
	return Key("\x1b" + s)
}

// Mod returns a named key with modifiers held, in the xterm modifier form: the
// key's CSI sequence with the parameter 1 plus the modifier bits. Mod(Up,
// ModCtrl) is CSI 1;5 A, Mod(Delete, ModShift) is CSI 3;2 ~, and Mod(F1,
// ModCtrl|ModShift) is CSI 1;6 P. Modifiers add up, so Mod(Mod(Up, ModCtrl),
// ModShift) is Mod(Up, ModCtrl|ModShift). A modified key is sent in this form
// whatever the cursor key mode, as xterm does.
//
// It applies to the named keys with a CSI or SS3 form: the cursor keys, Home,
// End, Insert, Delete, PageUp, PageDown and F1 to F12. Any other key is
// returned as it is, since it has no xterm modifier form. Use Ctrl for a
// control character and Alt for the ESC prefix.
func Mod(k Key, mods KeyMods) Key {
	if m, ok := withMods(k, mods); ok {
		return m
	}
	return k
}

// withMods adds modifier bits to a named key, and reports false for a key that
// has no xterm modifier form. The bits of KeyMods are the xterm bits: Shift 1,
// Alt 2, Ctrl 4. The wire parameter is the bits plus one.
func withMods(k Key, mods KeyMods) (Key, bool) {
	s := string(k)
	var (
		num   string // the number before the parameter: "1" for a letter key
		final byte
		have  int // the modifier bits k already carries
	)
	switch {
	case len(s) == 3 && strings.HasPrefix(s, "\x1bO") && strings.IndexByte("ABCDHFPQRS", s[2]) >= 0:
		// SS3: an application cursor key or F1 to F4. SS3 has no room for a
		// parameter, so the modified key uses the CSI form.
		num, final = "1", s[2]
	case strings.HasPrefix(s, "\x1b[") && len(s) >= 3:
		body, f := s[2:len(s)-1], s[len(s)-1]
		params := strings.Split(body, ";")
		switch {
		case f == '~' && len(params) <= 2 && isDigits(params[0]):
			num = params[0]
		case strings.IndexByte("ABCDHFPQRS", f) >= 0 && (body == "" || params[0] == "1" && len(params) == 2):
			// CSI P to CSI S name F1 to F4 only with a modifier; bare,
			// they are other sequences.
			if body == "" && strings.IndexByte("PQRS", f) >= 0 {
				return "", false
			}
			num = "1"
		default:
			return "", false
		}
		if len(params) == 2 {
			p, err := strconv.Atoi(params[1])
			if err != nil || p < 1 {
				return "", false
			}
			have = p - 1
		}
		final = f
	default:
		return "", false
	}
	bits := have | int(mods)
	if bits == 0 {
		return k, true
	}
	return Key("\x1b[" + num + ";" + strconv.Itoa(bits+1) + string(final)), true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// keyString flattens one SendKeys item into the bytes to send. A Key is a
// keystroke, written in the legacy encoding, and is sent the way a terminal
// sends that key under the modes the program set (see keyenc.Reencode).
// Strings and runes are text and are sent as they are.
func keyString(item any, m keyenc.Modes) (string, error) {
	switch v := item.(type) {
	case Key:
		return string(keyenc.Reencode([]byte(v), m)), nil
	case string:
		return v, nil
	case rune:
		return string(v), nil
	case []Key:
		var s strings.Builder
		for _, k := range v {
			ks, _ := keyString(k, m)
			s.WriteString(ks)
		}
		return s.String(), nil
	case []string:
		return strings.Join(v, ""), nil
	case []any:
		var s strings.Builder
		for _, k := range v {
			ks, err := keyString(k, m)
			if err != nil {
				return "", err
			}
			s.WriteString(ks)
		}
		return s.String(), nil
	default:
		return "", fmt.Errorf("tuitest: unsupported key item %T", item)
	}
}

// Bracketed paste delimiters (DEC private mode 2004). A terminal wraps pasted
// text in these so a program can tell a paste from typing.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// Paste sends text wrapped in bracketed-paste markers, the way a terminal
// delivers a real paste to a program that enabled mode 2004. Such programs take
// a different code path for pasted text than for typed text, and that path is
// often the less tested one.
//
// The markers are sent whether or not the program enabled the mode, so a test
// can check how a program copes with markers it did not ask for. A real
// terminal sends a paste to such a program as plain text; to reproduce that,
// use Type.
func (t *Terminal) Paste(s string) error {
	return t.write("Paste", []byte(pasteStart+s+pasteEnd))
}

// SendKeys types a sequence of named keys, chords, runes, and strings. Plain
// strings and runes are sent literally; Key values carry their own escape
// sequences. Items may be string, rune, Key, []string, []Key, or []any of
// those; anything else is rejected with an error rather than sent. Example:
//
//	term.SendKeys("git status", tuitest.Enter)
//	term.SendKeys(tuitest.Ctrl('b'), "%")
//
// A Key is sent the way a terminal sends that key under the keyboard modes the
// program has set, the way a user's terminal would:
//
//   - With no mode set, a Key is sent as its constant says.
//   - Once the program has set DECCKM (mode 1), the unmodified arrow keys, Home
//     and End are sent as SS3 sequences (ESC O A), which full-screen programs
//     built on terminfo set at startup and then match against.
//   - Once the program has pushed kitty keyboard flags (CSI > flags u), keys
//     follow the kitty protocol for those flags: Ctrl('b') is CSI 98;5 u, Esc
//     is CSI 27 u, and with the flag that reports every key Enter is CSI 13 u.
//   - At modifyOtherKeys level 2 (CSI > 4;2 m), a key with Ctrl or Alt is sent
//     as CSI 27;mod;code ~.
//
// Strings and runes are text and are sent as they are, whatever the modes: use
// Key values for keystrokes. Only key presses are sent, never key releases.
func (t *Terminal) SendKeys(items ...any) error {
	// After a panic the modes stay zero, and the write below returns the
	// panic error.
	var m keyenc.Modes
	t.mu.Lock()
	t.emuLocked("reading the keyboard modes", nil, func() {
		m.AppCursor = t.emu.ApplicationCursorKeys()
		m.KittyFlags = t.emu.KittyKeyboardFlags()
		m.ModifyOtherKeys = t.emu.ModifyOtherKeys()
	})
	t.mu.Unlock()
	var buf []byte
	for _, item := range items {
		s, err := keyString(item, m)
		if err != nil {
			return err
		}
		buf = append(buf, s...)
	}
	return t.write("SendKeys", buf)
}
