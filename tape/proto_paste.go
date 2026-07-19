package tape

import (
	"bytes"
	"strings"
)

func init() { Register(bracketedPaste{}) }

// bracketedPaste decodes mode 2004 pastes: the pasted text arrives wrapped in
// CSI 200 ~ and CSI 201 ~ so a program can tell it from typing.
//
// The guard is the interesting part. A terminal is supposed to strip the end
// marker out of the payload before sending it, precisely so that pasting text
// which happens to contain the marker cannot make the rest of the paste look
// like typing to the program. A recorder has to take the same position: the
// first end marker ends the paste, full stop. Anything after it is separate
// input and is decoded on its own terms.
//
// The matching restriction on the encode side is that a Paste command whose
// text contains a marker has no faithful wire form, since sending it would
// produce bytes that decode back to something shorter. Encode declines those,
// and the parser rejects them outright with a message pointing at Raw, so the
// injection cannot be smuggled in through a hand-written tape either.
type bracketedPaste struct{}

func (bracketedPaste) Name() string       { return "bracketed-paste" }
func (bracketedPaste) Priority() int      { return 0 }
func (bracketedPaste) Fidelity() Fidelity { return Exact }

// Keyboard reports false, so the registry rejects this protocol if it ever
// emits a Key or Type command. These bytes are reports from the terminal,
// never something the user pressed.
func (bracketedPaste) Keyboard() bool { return false }

func (bracketedPaste) Decode(buf []byte, _ Modes) (int, []Command, Result) {
	start := []byte(pasteStart)
	if len(buf) < len(start) {
		if bytes.HasPrefix(start, buf) {
			return 0, nil, Partial
		}
		return 0, nil, NoMatch
	}
	if !bytes.HasPrefix(buf, start) {
		return 0, nil, NoMatch
	}

	body := buf[len(start):]
	end := bytes.Index(body, []byte(pasteEnd))
	if end < 0 {
		// The paste has not finished arriving. Holding is what keeps a large
		// paste split across many reads from being decoded as typed text.
		return 0, nil, Partial
	}

	text := string(body[:end])
	n := len(start) + end + len(pasteEnd)
	return n, []Command{{Kind: KindPaste, Text: text}}, Full
}

func (bracketedPaste) Encode(c Command, _ Modes) ([]byte, bool) {
	if c.Kind != KindPaste {
		return nil, false
	}
	if !PasteTextIsSafe(c.Text) {
		return nil, false
	}
	return []byte(pasteStart + c.Text + pasteEnd), true
}

// PasteTextIsSafe reports whether text can be sent as a bracketed paste without
// the receiving program seeing a different paste than the one intended.
//
// Text containing either bracketed-paste marker cannot: the end marker would
// terminate the paste early, leaving the remainder to be read as typed input,
// which is the paste-injection problem that bracketed paste exists to prevent.
func PasteTextIsSafe(text string) bool {
	return !strings.Contains(text, pasteStart) && !strings.Contains(text, pasteEnd)
}

// pasteStart and pasteEnd mirror the constants in the root package. They are
// duplicated rather than exported from there because they are wire syntax this
// protocol owns, and a protocol is meant to be a self-contained unit.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)
