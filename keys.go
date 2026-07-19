package tuitest

import "fmt"

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

// Ctrl returns the control-key byte for a rune, so Ctrl('b') is 0x02. Letters
// are case-insensitive.
func Ctrl(r rune) Key {
	if r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	return Key([]byte{byte(r) & 0x1f})
}

// Alt prefixes a key or rune with ESC, the conventional meta encoding. It
// accepts the same items as SendKeys; an item of an unsupported type yields a
// bare Esc, since Alt has no way to report an error. Pass a string, rune, Key,
// or slice of those and that cannot happen.
func Alt(k any) Key {
	s, err := keyString(k)
	if err != nil {
		return Esc
	}
	return Key("\x1b" + s)
}

func keyString(item any) (string, error) {
	switch v := item.(type) {
	case Key:
		return string(v), nil
	case string:
		return v, nil
	case rune:
		return string(v), nil
	case []Key:
		var s string
		for _, k := range v {
			s += string(k)
		}
		return s, nil
	case []string:
		var s string
		for _, k := range v {
			s += k
		}
		return s, nil
	case []any:
		var s string
		for _, k := range v {
			ks, err := keyString(k)
			if err != nil {
				return "", err
			}
			s += ks
		}
		return s, nil
	default:
		return "", fmt.Errorf("tuitest: unsupported key item %T", item)
	}
}

// SendKeys types a sequence of named keys, chords, runes, and strings. Plain
// strings and runes are sent literally; Key values carry their own escape
// sequences. Items may be string, rune, Key, []string, []Key, or []any of
// those; anything else is rejected with an error rather than sent. Example:
//
//	term.SendKeys("git status", tuitest.Enter)
//	term.SendKeys(tuitest.Ctrl('b'), "%")
func (t *Terminal) SendKeys(items ...any) error {
	var buf []byte
	for _, item := range items {
		s, err := keyString(item)
		if err != nil {
			return err
		}
		buf = append(buf, s...)
	}
	return t.write(buf)
}
