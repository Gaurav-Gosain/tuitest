package tape

import "github.com/Gaurav-Gosain/tuitest/internal/keyenc"

func init() { Register(modifyOtherKeys{}) }

// modifyOtherKeys decodes xterm's modifyOtherKeys encoding, CSI 27 ; mod ; code
// ~, which exists so a program can tell Ctrl+I from Tab and Ctrl+M from Enter.
// The legacy encoding collapses those onto the same control byte, which is why
// xterm added a form that reports the modifiers separately from the key.
//
// It is Canonical: the tape records the key and its modifiers, and re-encoding
// always produces the CSI 27 form even when the recording used a redundant
// spelling such as an omitted default modifier.
type modifyOtherKeys struct{}

func (modifyOtherKeys) Name() string       { return "modify-other-keys" }
func (modifyOtherKeys) Priority() int      { return 10 }
func (modifyOtherKeys) Fidelity() Fidelity { return Canonical }

// Keyboard reports true: this protocol decodes keystrokes.
func (modifyOtherKeys) Keyboard() bool { return true }

func (p modifyOtherKeys) Decode(buf []byte, m Modes) (int, []Command, Result) {
	body, final, n, r := csiFrame(buf)
	if r != Full {
		return 0, nil, r
	}
	if final != '~' {
		return 0, nil, NoMatch
	}

	params, ok := parseCSIParams(string(body))
	if !ok || len(params) != 3 || params.at(0, 0, -1) != 27 {
		return 0, nil, NoMatch
	}

	mask := params.at(1, 0, 1) - 1
	code := params.at(2, 0, -1)
	if mask < 0 || code < 0 {
		return 0, nil, NoMatch
	}

	tok, ok := keyenc.ChordToken(mask, rune(code))
	if !ok {
		return 0, nil, NoMatch
	}
	cmd := Command{Kind: KindKey, Keys: []string{tok}}
	cmd.Modes.ModifyOtherKeys = max(m.ModifyOtherKeys, 1)
	return n, []Command{cmd}, Full
}

func (p modifyOtherKeys) Encode(c Command, m Modes) ([]byte, bool) {
	// Only claim a command when modifyOtherKeys is actually in force, so an
	// ordinary recording keeps its ordinary spelling.
	if c.Kind != KindKey || c.KeyAttrs.set() {
		return nil, false
	}
	if m.ModifyOtherKeys == 0 && c.Modes.ModifyOtherKeys == 0 {
		return nil, false
	}
	var out []byte
	for _, tok := range c.Keys {
		b, ok := keyenc.EncodeModifyOtherKeys(tok)
		if !ok {
			return nil, false
		}
		out = append(out, b...)
	}
	return out, true
}
