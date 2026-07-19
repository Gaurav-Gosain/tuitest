package tape

import (
	"fmt"
	"strings"
	"testing"
)

// decodeOne decodes exactly one sequence and returns the single command it
// produced, failing if the input did not decode to exactly one command.
func decodeOne(t *testing.T, in string, m Modes) Command {
	t.Helper()
	d := inputDecoder{modes: m}
	d.feed([]byte(in))
	d.close()
	cmds := d.take()
	if len(cmds) != 1 {
		t.Fatalf("decode %q: got %d commands, want 1:\n%s",
			in, len(cmds), strings.TrimRight(Sprint(cmds), "\n"))
	}
	return cmds[0]
}

// TestLegacyKeyTable checks the legacy encoding of every named key against the
// byte sequence a terminal actually sends, so a typo in the table is caught
// here rather than by a test that silently agrees with the bug.
func TestLegacyKeyTable(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"Up", "\x1b[A"},
		{"Down", "\x1b[B"},
		{"Right", "\x1b[C"},
		{"Left", "\x1b[D"},
		{"Home", "\x1b[H"},
		{"End", "\x1b[F"},
		{"Insert", "\x1b[2~"},
		{"Delete", "\x1b[3~"},
		{"PageUp", "\x1b[5~"},
		{"PageDown", "\x1b[6~"},
		{"F1", "\x1bOP"},
		{"F2", "\x1bOQ"},
		{"F3", "\x1bOR"},
		{"F4", "\x1bOS"},
		{"F5", "\x1b[15~"},
		{"F12", "\x1b[24~"},
		{"Enter", "\r"},
		{"Tab", "\t"},
		{"Esc", "\x1b"},
		{"Backspace", "\x7f"},
		{"Ctrl+a", "\x01"},
		{"Ctrl+@", "\x00"},
		{"Ctrl+]", "\x1d"},
		{"Alt+x", "\x1bx"},
		{"Alt+Backspace", "\x1b\x7f"},
		{"Ctrl+Alt+a", "\x1b\x01"},

		// Modified named keys take the xterm parameter, which is the
		// modifier bitmask plus one.
		{"Shift+Up", "\x1b[1;2A"},
		{"Alt+Up", "\x1b[1;3A"},
		{"Ctrl+Up", "\x1b[1;5A"},
		{"Ctrl+Shift+Up", "\x1b[1;6A"},
		{"Ctrl+Alt+Shift+Up", "\x1b[1;8A"},
		{"Super+Up", "\x1b[1;9A"},
		{"Ctrl+Delete", "\x1b[3;5~"},
		{"Shift+F5", "\x1b[15;2~"},

		// SS3 has nowhere to put a parameter, so a modified function key
		// is promoted to the CSI form, which is what terminals emit.
		{"Ctrl+F1", "\x1b[1;5P"},
	}

	for _, tc := range tests {
		t.Run(tc.token, func(t *testing.T) {
			got, err := ResolveKey(tc.token)
			if err != nil {
				t.Fatalf("ResolveKey(%q): %v", tc.token, err)
			}
			if string(got) != tc.want {
				t.Fatalf("ResolveKey(%q) = %q, want %q", tc.token, got, tc.want)
			}
			if c := decodeOne(t, tc.want, Modes{}); c.Kind != KindKey || c.Keys[0] != tc.token {
				t.Fatalf("decode %q = %v %v, want Key %s", tc.want, c.Kind, c.Keys, tc.token)
			}
		})
	}
}

// TestApplicationCursorMode checks that DECCKM changes the spelling of the
// cursor keys in both directions. This is the clearest case of the same key
// having two encodings depending on a negotiated mode.
func TestApplicationCursorMode(t *testing.T) {
	app := Modes{AppCursor: true}

	for _, name := range []string{"Up", "Down", "Left", "Right", "Home", "End"} {
		d := keyByName[name]
		normal := "\x1b[" + string(d.final)
		application := "\x1bO" + string(d.final)

		if got, _ := ResolveKeyModes(name, Modes{}); string(got) != normal {
			t.Errorf("%s under normal cursor mode = %q, want %q", name, got, normal)
		}
		if got, _ := ResolveKeyModes(name, app); string(got) != application {
			t.Errorf("%s under DECCKM = %q, want %q", name, got, application)
		}
		// Both spellings must decode to the same key, since a program
		// that sent either meant the same keypress.
		for _, in := range []string{normal, application} {
			if c := decodeOne(t, in, app); c.Keys[0] != name {
				t.Errorf("decode %q = %v, want %s", in, c.Keys, name)
			}
		}
	}
}

// TestModifierMatrixRoundTrips walks every named key against every combination
// of the modifiers the legacy encoding can carry, and checks that encoding then
// decoding returns the same chord. This is the command direction of the
// round-trip property over the whole matrix rather than a few examples.
func TestModifierMatrixRoundTrips(t *testing.T) {
	mods := []int{modShift, modAlt, modCtrl, modSuper, modHyper, modMeta}

	for _, d := range keyDefs {
		for mask := 0; mask < 1<<len(mods); mask++ {
			bits := 0
			for i, bit := range mods {
				if mask&(1<<i) != 0 {
					bits |= bit
				}
			}
			token := modString(bits) + d.name

			t.Run(token, func(t *testing.T) {
				seq, err := ResolveKey(token)
				if err != nil {
					t.Fatalf("ResolveKey: %v", err)
				}
				c := decodeOne(t, string(seq), Modes{})
				if c.Kind != KindKey {
					t.Fatalf("%q decoded as %v, want Key", seq, c.Kind)
				}
				if c.Keys[0] != token {
					t.Fatalf("%q decoded as %q, want %q", seq, c.Keys[0], token)
				}
			})
		}
	}
}

// TestKittyKeyboard covers the kitty CSI u encoding including the parts no
// other protocol can express: release and repeat events, the shifted and base
// layouts of the physical key, and associated text.
func TestKittyKeyboard(t *testing.T) {
	kitty := Modes{KittyFlags: 1}

	tests := []struct {
		name  string
		in    string
		key   string
		attrs KeyAttrs
	}{
		{"plain letter", "\x1b[97u", "a", KeyAttrs{}},
		{"ctrl letter", "\x1b[97;5u", "Ctrl+a", KeyAttrs{}},
		{"release", "\x1b[97;1:3u", "a", KeyAttrs{Event: KeyRelease}},
		{"repeat", "\x1b[97;1:2u", "a", KeyAttrs{Event: KeyRepeat}},
		{"ctrl release", "\x1b[97;5:3u", "Ctrl+a", KeyAttrs{Event: KeyRelease}},
		{"shifted layout", "\x1b[97:65;2u", "Shift+a", KeyAttrs{Shifted: "A"}},
		{"base layout", "\x1b[97::98u", "a", KeyAttrs{Base: "b"}},
		{"associated text", "\x1b[97;;97u", "a", KeyAttrs{Text: "a"}},
		{"multi rune text", "\x1b[97;;104:105u", "a", KeyAttrs{Text: "hi"}},
		{"escape key", "\x1b[27u", "Esc", KeyAttrs{}},
		{"enter key", "\x1b[13u", "Enter", KeyAttrs{}},
		{"functional up", "\x1b[57352u", "Up", KeyAttrs{}},
		{"functional up release", "\x1b[57352;1:3u", "Up", KeyAttrs{Event: KeyRelease}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := decodeOne(t, tc.in, kitty)
			if c.Kind != KindKey {
				t.Fatalf("decoded as %v, want Key", c.Kind)
			}
			if c.Keys[0] != tc.key {
				t.Fatalf("key = %q, want %q", c.Keys[0], tc.key)
			}
			if c.KeyAttrs != tc.attrs {
				t.Fatalf("attrs = %+v, want %+v", c.KeyAttrs, tc.attrs)
			}
			// Byte direction: this spelling re-encodes exactly.
			out, err := encodeCommands([]Command{c})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(out) != tc.in {
				t.Fatalf("re-encoded as %q, want %q", out, tc.in)
			}
		})
	}
}

// TestKittyEventMatrixRoundTrips checks every key against every event type and
// modifier combination under kitty, which is the matrix the protocol adds over
// the legacy one.
func TestKittyEventMatrixRoundTrips(t *testing.T) {
	kitty := Modes{KittyFlags: 1}
	events := []KeyEvent{KeyPress, KeyRepeat, KeyRelease}
	masks := []int{0, modShift, modCtrl, modAlt | modCtrl, modSuper | modShift}

	for _, d := range keyDefs {
		for _, ev := range events {
			for _, mask := range masks {
				token := modString(mask) + d.name
				attrs := KeyAttrs{Event: ev}
				name := fmt.Sprintf("%s/%s", token, ev)

				t.Run(name, func(t *testing.T) {
					cmd := Command{
						Kind: KindKey, Keys: []string{token},
						KeyAttrs: attrs, Modes: kitty,
					}
					out, err := encodeCommands([]Command{cmd})
					if err != nil {
						t.Fatalf("encode: %v", err)
					}
					got := decodeOne(t, string(out), kitty)
					if got.Keys[0] != token {
						t.Fatalf("%q decoded as %q, want %q", out, got.Keys[0], token)
					}
					if got.KeyAttrs != attrs {
						t.Fatalf("%q decoded attrs %+v, want %+v", out, got.KeyAttrs, attrs)
					}
				})
			}
		}
	}
}

// TestModifyOtherKeys covers xterm's CSI 27 form, which exists so a program can
// tell Ctrl+I from Tab. Both send 0x09 in the legacy encoding, so without this
// protocol the distinction is unrecoverable.
func TestModifyOtherKeys(t *testing.T) {
	mok := Modes{ModifyOtherKeys: 2}

	tests := []struct {
		in  string
		key string
	}{
		{"\x1b[27;5;97~", "Ctrl+a"},
		{"\x1b[27;5;105~", "Ctrl+i"},
		{"\x1b[27;5;109~", "Ctrl+m"},
		// Shift on a letter is spelled with a lowercase base, because Shift is
		// what makes it uppercase. "Shift+A" names the shift twice and the key
		// resolver rejects it, so decoding to it would write a tape that fails
		// to parse on the next run.
		{"\x1b[27;2;65~", "Shift+a"},
		{"\x1b[27;1;13~", "Enter"},
		{"\x1b[27;5;13~", "Ctrl+Enter"},
		{"\x1b[27;5;9~", "Ctrl+Tab"},
		{"\x1b[27;3;32~", "Alt+Space"},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			c := decodeOne(t, tc.in, mok)
			if c.Kind != KindKey || c.Keys[0] != tc.key {
				t.Fatalf("decode %q = %v %v, want Key %s", tc.in, c.Kind, c.Keys, tc.key)
			}
			out, err := encodeCommands([]Command{c})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(out) != tc.in {
				t.Fatalf("re-encoded as %q, want %q", out, tc.in)
			}
		})
	}
}

// TestChordsWithNoUnnegotiatedSpellingStillResolve covers the chords that exist
// only under a negotiated protocol. Ctrl+Enter is much of the reason
// modifyOtherKeys exists, since the legacy encoding folds it onto plain Enter,
// and a tape file carries no mode context.
//
// Resolving these strictly under default modes would fail, which would force
// the recorder to write them as Raw and lose the name. The encoder instead
// falls back to the protocol that invented the chord, so the tape stays
// readable and still replays the bytes the program will understand.
func TestChordsWithNoUnnegotiatedSpellingStillResolve(t *testing.T) {
	for _, chord := range []string{"Ctrl+Enter", "Ctrl+Tab", "Ctrl++"} {
		t.Run(chord, func(t *testing.T) {
			got, err := ResolveKey(chord)
			if err != nil {
				t.Fatalf("ResolveKey(%q) = %v, want it to resolve", chord, err)
			}
			// The bytes must be some negotiated protocol's, not a legacy
			// spelling that would silently mean a different key.
			if len(got) == 0 || got[0] != 0x1b {
				t.Fatalf("ResolveKey(%q) = %q, want an escape sequence", chord, got)
			}
			// And they must read back as the chord that was asked for, which
			// is what makes the fallback faithful rather than merely non-empty.
			c := decodeOne(t, string(got), permissiveModes)
			if c.Kind != KindKey || c.Keys[0] != chord {
				t.Fatalf("%q decoded back as %v %v, want Key %s", got, c.Kind, c.Keys, chord)
			}
		})
	}
}

// TestCtrlWithoutALegacyEncoding pins which Ctrl chords the legacy encoding can
// actually carry. Ctrl and '+' would send 0x0b, which reads back as Ctrl+k, so
// encoding it that way would silently change the key. It has no legacy
// spelling, and belongs to modifyOtherKeys or kitty.
func TestCtrlWithoutALegacyEncoding(t *testing.T) {
	// Ctrl and '+' has no legacy spelling: 0x0b reads back as Ctrl+k, so
	// encoding it that way would silently change the key. The resolver must
	// therefore not produce that byte. It does resolve, through the fallback to
	// a negotiated protocol that can express the chord faithfully.
	got, err := ResolveKey("Ctrl++")
	if err != nil {
		t.Fatalf("ResolveKey(Ctrl++): %v", err)
	}
	if string(got) == "\x0b" {
		t.Error("Ctrl++ resolved to 0x0b, which decodes back as Ctrl+k")
	}

	// Under kitty it has a faithful encoding, and it round-trips.
	kitty := Modes{KittyFlags: 1}
	seq, err := ResolveKeyModes("Ctrl++", kitty)
	if err != nil {
		t.Fatalf("Ctrl++ under kitty: %v", err)
	}
	if c := decodeOne(t, string(seq), kitty); c.Keys[0] != "Ctrl++" {
		t.Errorf("Ctrl++ under kitty decoded as %q", c.Keys[0])
	}

	// The chords that do have a legacy encoding still work.
	for tok, want := range map[string]string{
		"Ctrl+a": "\x01", "Ctrl+@": "\x00", "Ctrl+[": "\x1b", "Ctrl+_": "\x1f",
	} {
		got, err := ResolveKey(tok)
		if err != nil || string(got) != want {
			t.Errorf("ResolveKey(%q) = %q, %v; want %q", tok, got, err, want)
		}
	}
}

// TestMetaCollidesWithControlStringIntroducers pins a real terminal ambiguity.
// The meta encoding of Alt puts ESC before the key, so Alt+Shift+p sends ESC P,
// which is also the DCS introducer, and Alt+Shift+x sends ESC X, which is also
// SOS. A terminal really does send those bytes, so encoding them is right.
//
// Reading them back, the decoder must prefer the control string. Guessing "key"
// is the reported defect in miniature: it turns a terminal reply into
// keystrokes, silently. Preferring the control string costs only readability,
// since the bytes still replay exactly as Raw.
func TestMetaCollidesWithControlStringIntroducers(t *testing.T) {
	for _, tok := range []string{"Alt+P", "Alt+X", "Alt+]", "Alt+_"} {
		seq, err := ResolveKey(tok)
		if err != nil {
			t.Fatalf("ResolveKey(%q): %v", tok, err)
		}

		cmds := decodeCommands([]byte(seq))
		for _, c := range cmds {
			if c.Kind == KindKey || c.Kind == KindType {
				t.Errorf("%q sends %q, which decoded as keyboard input %v; "+
					"a control-string introducer must win", tok, seq, c.Keys)
			}
		}

		// Whatever it decodes to, the bytes must survive.
		got, err := encodeCommands(cmds)
		if err != nil {
			t.Fatalf("%q: re-encode: %v", tok, err)
		}
		if string(got) != string(seq) {
			t.Errorf("%q: bytes %q did not survive, got %q", tok, seq, got)
		}
	}

	// Under kitty the same chords are unambiguous and round-trip as keys.
	kitty := Modes{KittyFlags: 1}
	for _, tok := range []string{"Alt+P", "Alt+X"} {
		seq, err := ResolveKeyModes(tok, kitty)
		if err != nil {
			t.Fatalf("%q under kitty: %v", tok, err)
		}
		if c := decodeOne(t, string(seq), kitty); c.Keys[0] != tok {
			t.Errorf("%q under kitty decoded as %q", tok, c.Keys[0])
		}
	}
}
