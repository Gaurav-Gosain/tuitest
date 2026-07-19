package tape

import (
	"fmt"
	"strconv"
	"strings"
)

// KeyEvent is the kind of key transition a report describes. Legacy keyboards
// can only report a press, so Press is the zero value and is never printed.
type KeyEvent int

const (
	// KeyPress is a key going down, and the only event legacy protocols report.
	KeyPress KeyEvent = iota
	// KeyRepeat is the terminal's auto-repeat of a held key.
	KeyRepeat
	// KeyRelease is a key coming up.
	KeyRelease
)

func (e KeyEvent) String() string {
	switch e {
	case KeyRepeat:
		return "Repeat"
	case KeyRelease:
		return "Release"
	default:
		return "Press"
	}
}

// KeyAttrs is the extra detail the kitty keyboard protocol can attach to a key
// report. Every field is optional and the zero value means "not reported",
// which is what a legacy key produces, so a plain chord prints exactly as it
// always has.
type KeyAttrs struct {
	// Event is the press, repeat or release transition.
	Event KeyEvent
	// Shifted is the character the key produces with shift applied, which
	// kitty reports so a program can distinguish layouts without guessing.
	Shifted string
	// Base is the character the key produces on the layout's base level,
	// used by programs that bind physical keys rather than characters.
	Base string
	// Text is the text the keypress would insert, which can differ from the
	// key itself for dead keys and input methods.
	Text string
}

// set reports whether any attribute is present, which decides whether a Key
// command needs its own line.
func (a KeyAttrs) set() bool {
	return a.Event != KeyPress || a.Shifted != "" || a.Base != "" || a.Text != ""
}

// write appends the attribute arguments in canonical order. The order is fixed
// so printing is deterministic and a tape diff stays readable.
func (a KeyAttrs) write(b *strings.Builder) {
	if a.Event != KeyPress {
		b.WriteString(" +")
		b.WriteString(a.Event.String())
	}
	if a.Shifted != "" {
		b.WriteString(" +Shifted ")
		b.WriteString(a.Shifted)
	}
	if a.Base != "" {
		b.WriteString(" +Base ")
		b.WriteString(a.Base)
	}
	if a.Text != "" {
		b.WriteString(" +Text ")
		b.WriteString(Quote(a.Text))
	}
}

// parseKeyAttr consumes one attribute starting at toks[i], returning the index
// just past it. It is shared by the parser so the Key verb and any future verb
// that carries key detail stay in step.
func parseKeyAttr(a *KeyAttrs, toks []token, i int) (int, error) {
	name := toks[i].text
	switch name {
	case "+Press":
		a.Event = KeyPress
		return i + 1, nil
	case "+Repeat":
		a.Event = KeyRepeat
		return i + 1, nil
	case "+Release":
		a.Event = KeyRelease
		return i + 1, nil
	case "+Shifted", "+Base":
		if i+1 >= len(toks) {
			return 0, fmt.Errorf("%s needs a character", name)
		}
		if name == "+Shifted" {
			a.Shifted = toks[i+1].text
		} else {
			a.Base = toks[i+1].text
		}
		return i + 2, nil
	case "+Text":
		if i+1 >= len(toks) {
			return 0, fmt.Errorf("+Text needs a quoted string")
		}
		s, err := strconv.Unquote(toks[i+1].text)
		if err != nil {
			return 0, fmt.Errorf("+Text needs a quoted string, got %s", toks[i+1].text)
		}
		a.Text = s
		return i + 2, nil
	}
	return 0, fmt.Errorf("unknown key attribute %s", name)
}

// parseKeyLine splits a Key line into its key tokens and attributes. An
// attribute is a token starting with '+' at the top level, which cannot be
// confused with a modifier because modifiers are joined to their key without
// spaces: "Ctrl+b" is one token, "+Release" is another.
//
// A line carrying attributes names exactly one key. Allowing several would make
// it ambiguous which key an attribute qualifies, and the whole point of the
// attribute is to say something specific about one keypress.
func parseKeyLine(toks []token) (keys []token, attrs KeyAttrs, err *ParseError) {
	for i := 0; i < len(toks); {
		if !strings.HasPrefix(toks[i].text, "+") || toks[i].text == "+" {
			keys = append(keys, toks[i])
			i++
			continue
		}
		next, aerr := parseKeyAttr(&attrs, toks, i)
		if aerr != nil {
			return nil, KeyAttrs{}, &ParseError{Col: toks[i].col, Msg: aerr.Error()}
		}
		i = next
	}

	if len(keys) == 0 {
		return nil, KeyAttrs{}, &ParseError{Msg: "Key needs at least one key name"}
	}
	if attrs.set() && len(keys) > 1 {
		return nil, KeyAttrs{}, &ParseError{
			Col: keys[1].col,
			Msg: "a Key line with attributes names exactly one key; split the others onto their own line",
		}
	}
	return keys, attrs, nil
}
