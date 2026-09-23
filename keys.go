package tuitest

import (
	"fmt"
	"strings"
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
// Ctrl('^') and Ctrl('_') are 0x1c to 0x1f as are Ctrl('4') to Ctrl('7'), and
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
	default:
		return Key(string(r))
	}
}

// Alt prefixes a key or rune with ESC, the conventional meta encoding. It
// accepts the same items as SendKeys; an item of an unsupported type yields a
// bare Esc, since Alt has no way to report an error. Pass a string, rune, Key,
// or slice of those and that cannot happen.
func Alt(k any) Key {
	s, err := keyString(k, false)
	if err != nil {
		return Esc
	}
	return Key("\x1b" + s)
}

// applicationCursorKeys maps each cursor key to what a terminal sends for it
// once the program has set DECCKM (mode 1): the same final byte, introduced by
// SS3 instead of CSI.
var applicationCursorKeys = map[Key]Key{
	Up:    "\x1bOA",
	Down:  "\x1bOB",
	Right: "\x1bOC",
	Left:  "\x1bOD",
	Home:  "\x1bOH",
	End:   "\x1bOF",
}

// keyString flattens one SendKeys item into the bytes to send. appCursor
// reports whether the program has set DECCKM, in which case the named cursor
// keys are sent in their SS3 form.
func keyString(item any, appCursor bool) (string, error) {
	switch v := item.(type) {
	case Key:
		if appCursor {
			if k, ok := applicationCursorKeys[v]; ok {
				return string(k), nil
			}
		}
		return string(v), nil
	case string:
		return v, nil
	case rune:
		return string(v), nil
	case []Key:
		var s strings.Builder
		for _, k := range v {
			ks, _ := keyString(k, appCursor)
			s.WriteString(ks)
		}
		return s.String(), nil
	case []string:
		return strings.Join(v, ""), nil
	case []any:
		var s strings.Builder
		for _, k := range v {
			ks, err := keyString(k, appCursor)
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
	return t.write([]byte(pasteStart + s + pasteEnd))
}

// SendKeys types a sequence of named keys, chords, runes, and strings. Plain
// strings and runes are sent literally; Key values carry their own escape
// sequences. Items may be string, rune, Key, []string, []Key, or []any of
// those; anything else is rejected with an error rather than sent. Example:
//
//	term.SendKeys("git status", tuitest.Enter)
//	term.SendKeys(tuitest.Ctrl('b'), "%")
//
// The arrow keys, Home and End are sent the way a terminal sends them in the
// program's current cursor key mode: as CSI sequences (ESC [ A) normally, and
// as SS3 sequences (ESC O A) once the program has set DECCKM (mode 1), which
// full-screen programs built on terminfo do at startup and then match against.
// Every other key is sent as its constant says.
func (t *Terminal) SendKeys(items ...any) error {
	t.mu.Lock()
	appCursor := t.emu.ApplicationCursorKeys()
	t.mu.Unlock()
	var buf []byte
	for _, item := range items {
		s, err := keyString(item, appCursor)
		if err != nil {
			return err
		}
		buf = append(buf, s...)
	}
	return t.write(buf)
}
