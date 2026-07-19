package tape

import (
	"sort"
	"strings"
)

// This file holds the one description of what keys exist and how they are
// spelled on the wire. The legacy, modifyOtherKeys and kitty protocols all read
// from it, so a key added here is understood by every protocol at once and the
// three can never drift into disagreeing about what "F5" means.

// keyForm is how a named key is encoded in the legacy CSI/SS3 grammar.
type keyForm int

const (
	// formLetter is CSI plus a final letter, as in CSI A for Up. With a
	// modifier it becomes CSI 1 ; mod A.
	formLetter keyForm = iota
	// formTilde is CSI plus a number and '~', as in CSI 15 ~ for F5. With a
	// modifier it becomes CSI 15 ; mod ~.
	formTilde
	// formSS3 is SS3 plus a final letter, as in SS3 P for F1. A modifier
	// promotes it to the CSI 1 ; mod P form, since SS3 has nowhere to put
	// parameters.
	formSS3
)

// keyDef describes one named key.
type keyDef struct {
	name string
	form keyForm
	// final is the final byte for formLetter and formSS3.
	final byte
	// num is the parameter for formTilde.
	num int
	// appCursor marks the cursor keys, which switch between CSI and SS3
	// introducers with DECCKM rather than having one fixed spelling.
	appCursor bool
	// kittyCode is the kitty functional-key codepoint, used by the CSI u
	// grammar. Zero means kitty spells this key with the legacy form.
	kittyCode int
}

// keyDefs is the master table. Order is irrelevant; lookups are by index built
// below.
var keyDefs = []keyDef{
	{name: "Up", form: formLetter, final: 'A', appCursor: true, kittyCode: 57352},
	{name: "Down", form: formLetter, final: 'B', appCursor: true, kittyCode: 57353},
	{name: "Right", form: formLetter, final: 'C', appCursor: true, kittyCode: 57351},
	{name: "Left", form: formLetter, final: 'D', appCursor: true, kittyCode: 57350},
	{name: "Home", form: formLetter, final: 'H', appCursor: true, kittyCode: 57356},
	{name: "End", form: formLetter, final: 'F', appCursor: true, kittyCode: 57357},

	{name: "Insert", form: formTilde, num: 2, kittyCode: 57348},
	{name: "Delete", form: formTilde, num: 3, kittyCode: 57349},
	{name: "PageUp", form: formTilde, num: 5, kittyCode: 57354},
	{name: "PageDown", form: formTilde, num: 6, kittyCode: 57355},

	{name: "F1", form: formSS3, final: 'P', kittyCode: 57364},
	{name: "F2", form: formSS3, final: 'Q', kittyCode: 57365},
	{name: "F3", form: formSS3, final: 'R', kittyCode: 57366},
	{name: "F4", form: formSS3, final: 'S', kittyCode: 57367},
	{name: "F5", form: formTilde, num: 15, kittyCode: 57368},
	{name: "F6", form: formTilde, num: 17, kittyCode: 57369},
	{name: "F7", form: formTilde, num: 18, kittyCode: 57370},
	{name: "F8", form: formTilde, num: 19, kittyCode: 57371},
	{name: "F9", form: formTilde, num: 20, kittyCode: 57372},
	{name: "F10", form: formTilde, num: 21, kittyCode: 57373},
	{name: "F11", form: formTilde, num: 23, kittyCode: 57374},
	{name: "F12", form: formTilde, num: 24, kittyCode: 57375},
}

var (
	keyByName   = map[string]keyDef{}
	keyByLetter = map[byte]keyDef{}
	keyByTilde  = map[int]keyDef{}
	keyBySS3    = map[byte]keyDef{}
	keyByKitty  = map[int]keyDef{}
)

func init() {
	for _, d := range keyDefs {
		keyByName[d.name] = d
		if d.kittyCode != 0 {
			keyByKitty[d.kittyCode] = d
		}
		switch d.form {
		case formLetter:
			keyByLetter[d.final] = d
		case formTilde:
			keyByTilde[d.num] = d
		case formSS3:
			keyBySS3[d.final] = d
		}
	}
	// Cursor keys are reachable through SS3 as well when DECCKM is set.
	for _, d := range keyDefs {
		if d.appCursor {
			keyBySS3[d.final] = d
		}
	}
}

// Modifier bits as xterm and kitty encode them: the wire parameter is the
// bitmask plus one, so an unmodified key sends 1.
const (
	modShift = 1 << iota
	modAlt
	modCtrl
	modSuper
	modHyper
	modMeta
	modCapsLock
	modNumLock
)

// modOrder fixes the spelling order of a chord so decoding is deterministic and
// a tape diff does not churn. It is the conventional reading order rather than
// the bit order.
var modOrder = []struct {
	bit  int
	name string
}{
	{modCtrl, "Ctrl"},
	{modAlt, "Alt"},
	{modShift, "Shift"},
	{modSuper, "Super"},
	{modHyper, "Hyper"},
	{modMeta, "Meta"},
	{modCapsLock, "CapsLock"},
	{modNumLock, "NumLock"},
}

// modAliases maps every accepted spelling of a modifier to its bit. The short
// forms exist because they were already accepted by ResolveKey.
var modAliases = func() map[string]int {
	m := map[string]int{"C": modCtrl, "M": modAlt, "S": modShift}
	for _, mo := range modOrder {
		m[mo.name] = mo.bit
	}
	return m
}()

// modString renders a modifier mask as the chord prefix, including the trailing
// '+', or "" when no modifiers are set.
func modString(mask int) string {
	var b strings.Builder
	for _, mo := range modOrder {
		if mask&mo.bit != 0 {
			b.WriteString(mo.name)
			b.WriteByte('+')
		}
	}
	return b.String()
}

// splitToken splits a key token into its modifier mask and base key. The base
// is the last '+'-separated field, which is why a literal '+' key is spelled as
// the last field and still parses.
func splitToken(tok string) (mask int, base string, ok bool) {
	parts := strings.Split(tok, "+")
	// A trailing empty field means the token ends in '+', so '+' is the base.
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = append(parts[:len(parts)-1], "+")
	}
	base = parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		bit, known := modAliases[p]
		if !known {
			return 0, "", false
		}
		mask |= bit
	}
	return mask, base, true
}

// namedKeyList returns the known key names sorted, for error messages that
// suggest what the writer might have meant.
func namedKeyList() []string {
	out := make([]string, 0, len(keyByName))
	for name := range keyByName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
