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
//
// It resolves under default modes, which is what a Key line means on its own.
// ResolveKeyModes takes the mode context when a recording captured one, since
// the cursor keys have a different spelling under DECCKM.
func ResolveKey(token string) (tuitest.Key, error) {
	return ResolveKeyModes(token, Modes{})
}

// ResolveKeyModes resolves a key token under an explicit mode context. It
// delegates to the same encoder the protocol registry uses, so a token the
// recorder produced always replays as the bytes it was decoded from; there is
// no second implementation that can drift.
func ResolveKeyModes(token string, m Modes) (tuitest.Key, error) {
	cmd := Command{Kind: KindKey, Keys: []string{token}, Modes: m}
	b, err := encodeCommand(cmd, Protocols())
	if err != nil {
		return "", keyTokenError(token)
	}
	return tuitest.Key(b), nil
}

// keyTokenError explains why a token did not resolve, distinguishing an unknown
// modifier from an unknown key from a modifier that the key cannot carry. The
// distinction is what makes the parse error actionable.
func keyTokenError(token string) error {
	parts := strings.Split(token, "+")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = append(parts[:len(parts)-1], "+")
	}
	base := parts[len(parts)-1]

	for _, mod := range parts[:len(parts)-1] {
		if _, ok := modAliases[mod]; !ok {
			return fmt.Errorf("unknown modifier %q", mod)
		}
	}

	if _, named := keyByName[base]; !named {
		switch base {
		case "Enter", "Return", "Tab", "Esc", "Escape", "Backspace", "Space":
		default:
			if utf8.RuneCountInString(base) != 1 {
				return fmt.Errorf("unknown key %q", base)
			}
		}
	}
	return fmt.Errorf("key %q cannot carry those modifiers", token)
}

func singleRune(s string) (rune, bool) {
	if utf8.RuneCountInString(s) != 1 {
		return 0, false
	}
	r, size := utf8.DecodeRuneInString(s)
	// A byte that is not valid UTF-8 decodes to RuneError with size 1 and
	// would otherwise pass as a one-rune key name, producing a token that
	// encodes to bytes nothing can decode back.
	if r == utf8.RuneError && size == 1 {
		return 0, false
	}
	return r, true
}
