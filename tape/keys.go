package tape

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Gaurav-Gosain/tuitest"
)

var namedKeys = map[string]tuitest.Key{
	"Enter":     tuitest.Enter,
	"Return":    tuitest.Enter,
	"Tab":       tuitest.Tab,
	"Esc":       tuitest.Esc,
	"Escape":    tuitest.Esc,
	"Space":     tuitest.Space,
	"Backspace": tuitest.Backspace,
	"Delete":    tuitest.Delete,
	"Insert":    tuitest.Insert,
	"Up":        tuitest.Up,
	"Down":      tuitest.Down,
	"Left":      tuitest.Left,
	"Right":     tuitest.Right,
	"Home":      tuitest.Home,
	"End":       tuitest.End,
	"PageUp":    tuitest.PageUp,
	"PageDown":  tuitest.PageDown,
	"F1":        tuitest.F1,
	"F2":        tuitest.F2,
	"F3":        tuitest.F3,
	"F4":        tuitest.F4,
	"F5":        tuitest.F5,
	"F6":        tuitest.F6,
	"F7":        tuitest.F7,
	"F8":        tuitest.F8,
	"F9":        tuitest.F9,
	"F10":       tuitest.F10,
	"F11":       tuitest.F11,
	"F12":       tuitest.F12,
}

// ResolveKey turns a tape key token such as "Enter", "Ctrl+b", "Alt+x", or a
// bare rune like "%" into the byte sequence a terminal would send.
func ResolveKey(token string) (tuitest.Key, error) {
	parts := strings.Split(token, "+")
	base := parts[len(parts)-1]
	mods := parts[:len(parts)-1]

	seq, err := resolveBase(base)
	if err != nil {
		return "", err
	}

	for _, mod := range mods {
		switch mod {
		case "Ctrl", "C":
			r, ok := singleRune(base)
			if !ok {
				return "", fmt.Errorf("Ctrl+ requires a single character, got %q", base)
			}
			seq = tuitest.Ctrl(r)
		case "Alt", "M":
			seq = tuitest.Alt(string(seq))
		case "Shift", "S":
			if r, ok := singleRune(base); ok && r >= 'a' && r <= 'z' {
				seq = tuitest.Key(strings.ToUpper(base))
			}
		default:
			return "", fmt.Errorf("unknown modifier %q", mod)
		}
	}
	return seq, nil
}

func resolveBase(base string) (tuitest.Key, error) {
	if k, ok := namedKeys[base]; ok {
		return k, nil
	}
	if utf8.RuneCountInString(base) == 1 {
		return tuitest.Key(base), nil
	}
	return "", fmt.Errorf("unknown key %q", base)
}

func singleRune(s string) (rune, bool) {
	if utf8.RuneCountInString(s) != 1 {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r, true
}
