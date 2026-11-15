package tape

import "github.com/Gaurav-Gosain/tuitest/internal/keyenc"

func init() { Register(legacyKeys{}) }

// legacyKeys decodes the keyboard encoding every terminal speaks without any
// negotiation: C0 control bytes, the ESC-prefixed meta encoding of Alt, and the
// CSI/SS3 cursor and function keys with their xterm modifier parameter.
//
// It is Canonical rather than Exact because the wire format has redundant
// spellings and the tape records the key rather than the spelling. The
// normalizations are:
//
//   - an omitted default parameter is restored, so CSI A and CSI 1 A both
//     decode to Up and re-encode as CSI A;
//   - a modified SS3 key is promoted to its CSI form, because SS3 has nowhere
//     to put a parameter, which is what real terminals do too;
//
// Each of these produces bytes that decode back to the same command, which is
// the Canonical contract and is what the round-trip fuzz target checks.
type legacyKeys struct{}

func (legacyKeys) Name() string       { return "legacy-keys" }
func (legacyKeys) Priority() int      { return 0 }
func (legacyKeys) Fidelity() Fidelity { return Canonical }

// Keyboard reports true: this protocol decodes keystrokes.
func (legacyKeys) Keyboard() bool { return true }

func (p legacyKeys) Decode(buf []byte, m Modes) (int, []Command, Result) {
	n, tok, r := keyenc.DecodeLegacy(buf, func(b []byte) ([]byte, byte, int, keyenc.Result) {
		body, final, n, r := csiFrame(b)
		return body, final, n, keyenc.Result(r)
	})
	if r != keyenc.Full {
		return 0, nil, Result(r)
	}
	return n, keyCmd(tok), Full
}

// Encode renders a Key command back to bytes. Multi-token Key lines encode as
// the concatenation of their tokens, which is exactly what the player sends.
func (p legacyKeys) Encode(c Command, m Modes) ([]byte, bool) {
	if c.Kind != KindKey || c.KeyAttrs.set() {
		return nil, false
	}
	var out []byte
	for _, tok := range c.Keys {
		b, ok := keyenc.EncodeLegacy(tok, m.AppCursor)
		if !ok {
			return nil, false
		}
		out = append(out, b...)
	}
	return out, true
}

// keyCmd is the one-token Key command shorthand the decoders return.
func keyCmd(tok string) []Command {
	return []Command{{Kind: KindKey, Keys: []string{tok}}}
}
