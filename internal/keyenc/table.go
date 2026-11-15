package keyenc

import (
	"strings"
	"unicode/utf8"
)

// Package keyenc is the one keyboard encoder in tuitest. It knows which keys
// exist, how each input protocol spells them (legacy, kitty, modifyOtherKeys),
// and how to read the legacy spelling back into a key token. The root package
// uses it to send keys the way the program asked for them, and the tape
// package uses it to decode and replay recordings, so the two cannot disagree
// about the bytes for a key.
//
// This file holds the one description of what keys exist and how they are
// spelled on the wire. The legacy, modifyOtherKeys and kitty protocols all read
// from it, so a key added here is understood by every protocol at once and the
// three can never drift into disagreeing about what "F5" means.

// Form is how a named key is encoded in the legacy CSI/SS3 grammar.
type Form int

const (
	// FormLetter is CSI plus a final letter, as in CSI A for Up. With a
	// modifier it becomes CSI 1 ; mod A.
	FormLetter Form = iota
	// FormTilde is CSI plus a number and '~', as in CSI 15 ~ for F5. With a
	// modifier it becomes CSI 15 ; mod ~.
	FormTilde
	// FormSS3 is SS3 plus a final letter, as in SS3 P for F1. A modifier
	// promotes it to the CSI 1 ; mod P form, since SS3 has nowhere to put
	// parameters.
	FormSS3
)

// KeyDef describes one named key.
type KeyDef struct {
	Name string
	Form Form
	// Final is the final byte for FormLetter and FormSS3.
	Final byte
	// Num is the parameter for FormTilde.
	Num int
	// AppCursor marks the cursor keys, which switch between CSI and SS3
	// introducers with DECCKM rather than having one fixed spelling.
	AppCursor bool
	// KittyCode is a CSI u code for this key. The decoder accepts it. The
	// encoder does not use it: the kitty keyboard protocol spells these keys
	// in their legacy form, with the modifier as a parameter.
	KittyCode int
}

// KeyDefs is the master table. Order is irrelevant; lookups are by index built
// below.
var KeyDefs = []KeyDef{
	{Name: "Up", Form: FormLetter, Final: 'A', AppCursor: true, KittyCode: 57352},
	{Name: "Down", Form: FormLetter, Final: 'B', AppCursor: true, KittyCode: 57353},
	{Name: "Right", Form: FormLetter, Final: 'C', AppCursor: true, KittyCode: 57351},
	{Name: "Left", Form: FormLetter, Final: 'D', AppCursor: true, KittyCode: 57350},
	{Name: "Home", Form: FormLetter, Final: 'H', AppCursor: true, KittyCode: 57356},
	{Name: "End", Form: FormLetter, Final: 'F', AppCursor: true, KittyCode: 57357},

	{Name: "Insert", Form: FormTilde, Num: 2, KittyCode: 57348},
	{Name: "Delete", Form: FormTilde, Num: 3, KittyCode: 57349},
	{Name: "PageUp", Form: FormTilde, Num: 5, KittyCode: 57354},
	{Name: "PageDown", Form: FormTilde, Num: 6, KittyCode: 57355},

	{Name: "F1", Form: FormSS3, Final: 'P', KittyCode: 57364},
	{Name: "F2", Form: FormSS3, Final: 'Q', KittyCode: 57365},
	{Name: "F3", Form: FormSS3, Final: 'R', KittyCode: 57366},
	{Name: "F4", Form: FormSS3, Final: 'S', KittyCode: 57367},
	{Name: "F5", Form: FormTilde, Num: 15, KittyCode: 57368},
	{Name: "F6", Form: FormTilde, Num: 17, KittyCode: 57369},
	{Name: "F7", Form: FormTilde, Num: 18, KittyCode: 57370},
	{Name: "F8", Form: FormTilde, Num: 19, KittyCode: 57371},
	{Name: "F9", Form: FormTilde, Num: 20, KittyCode: 57372},
	{Name: "F10", Form: FormTilde, Num: 21, KittyCode: 57373},
	{Name: "F11", Form: FormTilde, Num: 23, KittyCode: 57374},
	{Name: "F12", Form: FormTilde, Num: 24, KittyCode: 57375},
}

var (
	KeyByName   = map[string]KeyDef{}
	KeyByLetter = map[byte]KeyDef{}
	KeyByTilde  = map[int]KeyDef{}
	KeyBySS3    = map[byte]KeyDef{}
	KeyByKitty  = map[int]KeyDef{}
)

func init() {
	for _, d := range KeyDefs {
		KeyByName[d.Name] = d
		if d.KittyCode != 0 {
			KeyByKitty[d.KittyCode] = d
		}
		switch d.Form {
		case FormLetter:
			KeyByLetter[d.Final] = d
		case FormTilde:
			KeyByTilde[d.Num] = d
		case FormSS3:
			KeyBySS3[d.Final] = d
		}
	}
	// F1 to F4 have a tilde form too: CSI 11 ~ to CSI 14 ~ in the VT220
	// tradition, and the kitty protocol sends F3 as CSI 13 ~ because CSI R
	// is the cursor position report. The decoders read all four. The
	// encoders spell these keys by name, so this does not change the bytes
	// they send.
	for i, name := range []string{"F1", "F2", "F3", "F4"} {
		KeyByTilde[11+i] = KeyByName[name]
	}
	// Cursor keys are reachable through SS3 as well when DECCKM is set.
	for _, d := range KeyDefs {
		if d.AppCursor {
			KeyBySS3[d.Final] = d
		}
	}
}

// Modifier bits as xterm and kitty encode them: the wire parameter is the
// bitmask plus one, so an unmodified key sends 1.
const (
	ModShift = 1 << iota
	ModAlt
	ModCtrl
	ModSuper
	ModHyper
	ModMeta
	ModCapsLock
	ModNumLock
)

// ModOrder fixes the spelling order of a chord so decoding is deterministic and
// a tape diff does not churn. It is the conventional reading order rather than
// the bit order.
var ModOrder = []struct {
	Bit  int
	Name string
}{
	{ModCtrl, "Ctrl"},
	{ModAlt, "Alt"},
	{ModShift, "Shift"},
	{ModSuper, "Super"},
	{ModHyper, "Hyper"},
	{ModMeta, "Meta"},
	{ModCapsLock, "CapsLock"},
	{ModNumLock, "NumLock"},
}

// ModAliases maps every accepted spelling of a modifier to its bit. The short
// forms exist because they were already accepted by ResolveKey.
var ModAliases = func() map[string]int {
	m := map[string]int{"C": ModCtrl, "M": ModAlt, "S": ModShift}
	for _, mo := range ModOrder {
		m[mo.Name] = mo.Bit
	}
	return m
}()

// ModString renders a modifier mask as the chord prefix, including the trailing
// '+', or "" when no modifiers are set.
func ModString(mask int) string {
	var b strings.Builder
	for _, mo := range ModOrder {
		if mask&mo.Bit != 0 {
			b.WriteString(mo.Name)
			b.WriteByte('+')
		}
	}
	return b.String()
}

// SplitToken splits a key token into its modifier mask and base key. The base
// is the last '+'-separated field, which is why a literal '+' key is spelled as
// the last field and still parses.
func SplitToken(tok string) (mask int, base string, ok bool) {
	if tok == "" {
		return 0, "", false
	}

	mods := ""
	if strings.HasSuffix(tok, "+") {
		// The token names '+' itself. What precedes it is the modifier
		// list, which must end with the '+' that separates it from the
		// base: "Ctrl++" is Ctrl and '+', while a bare "Ctrl+" names no
		// key at all and is rejected.
		base = "+"
		mods = tok[:len(tok)-1]
		if mods != "" {
			if !strings.HasSuffix(mods, "+") {
				return 0, "", false
			}
			mods = mods[:len(mods)-1]
		}
	} else {
		i := strings.LastIndex(tok, "+")
		if i < 0 {
			return 0, tok, true
		}
		base, mods = tok[i+1:], tok[:i]
	}

	if base == "" {
		return 0, "", false
	}
	// A base containing whitespace has no spelling a tape file can carry,
	// because a Key line is tokenized on whitespace: "Shift+ " would be read
	// back as "Shift+" and fail to resolve. The space key is spelled "Space",
	// and rejecting the literal here keeps the one key from having two
	// spellings that different protocols encode differently.
	// A base containing whitespace has no spelling a tape file can carry,
	// because a Key line is tokenized on whitespace: "Shift+ " would be read
	// back as "Shift+" and fail to resolve. The space key is spelled "Space",
	// and rejecting the literal here keeps the one key from having two
	// spellings that different protocols encode differently.
	if strings.ContainsAny(base, " \t\r\n") {
		return 0, "", false
	}
	if mods != "" {
		for _, p := range strings.Split(mods, "+") {
			bit, known := ModAliases[p]
			if !known {
				return 0, "", false
			}
			mask |= bit
		}
	}
	return mask, base, true
}

// SingleRune reports whether s is exactly one valid rune, and returns it.
func SingleRune(s string) (rune, bool) {
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
