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
		{"\x1b[27;2;65~", "Shift+A"},
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

// TestCtrlIIsDistinguishableFromTab is the point of modifyOtherKeys stated as a
// property: under the legacy encoding the two keys are the same byte, and under
// modifyOtherKeys they must decode differently.
func TestCtrlIIsDistinguishableFromTab(t *testing.T) {
	tab := decodeOne(t, "\t", Modes{})
	if tab.Keys[0] != "Tab" {
		t.Fatalf("0x09 = %q, want Tab", tab.Keys[0])
	}
	ctrlI := decodeOne(t, "\x1b[27;5;105~", Modes{ModifyOtherKeys: 2})
	if ctrlI.Keys[0] != "Ctrl+i" {
		t.Fatalf("modifyOtherKeys Ctrl+i = %q, want Ctrl+i", ctrlI.Keys[0])
	}
}

// TestKeyAttributesSurviveTheTapeFile checks that a Key line carrying
// attributes prints and re-parses unchanged. Decoding a release event is
// worthless if writing it to a file loses it.
func TestKeyAttributesSurviveTheTapeFile(t *testing.T) {
	cases := []Command{
		{Kind: KindKey, Keys: []string{"a"}, KeyAttrs: KeyAttrs{Event: KeyRelease}},
		{Kind: KindKey, Keys: []string{"Ctrl+a"}, KeyAttrs: KeyAttrs{Event: KeyRepeat}},
		{Kind: KindKey, Keys: []string{"a"}, KeyAttrs: KeyAttrs{Shifted: "A", Base: "a"}},
		{Kind: KindKey, Keys: []string{"a"}, KeyAttrs: KeyAttrs{Text: "á"}},
		{Kind: KindKey, Keys: []string{"Up"}, KeyAttrs: KeyAttrs{Event: KeyRelease, Text: "x"}},
	}

	for _, c := range cases {
		src := Sprint([]Command{c})
		got, err := Parse(strings.NewReader(src))
		if err != nil {
			t.Fatalf("re-parse %q: %v", src, err)
		}
		if len(got) != 1 {
			t.Fatalf("re-parse %q gave %d commands", src, len(got))
		}
		if got[0].KeyAttrs != c.KeyAttrs {
			t.Errorf("%q: attrs %+v, want %+v", src, got[0].KeyAttrs, c.KeyAttrs)
		}
		if got[0].Keys[0] != c.Keys[0] {
			t.Errorf("%q: key %q, want %q", src, got[0].Keys[0], c.Keys[0])
		}
	}
}

// TestKeyAttributesNeedASingleKey checks the grammar rule that keeps attributes
// unambiguous: an attribute qualifies one keypress, so a line carrying one may
// not name several keys.
func TestKeyAttributesNeedASingleKey(t *testing.T) {
	_, err := Parse(strings.NewReader("Key a b +Release\n"))
	if err == nil {
		t.Fatal("a multi-key line with attributes parsed, want an error")
	}
	if !strings.Contains(err.Error(), "exactly one key") {
		t.Errorf("error does not explain the rule: %v", err)
	}
}

// TestCursorPositionReportIsNotF3 pins the ambiguity that CSI R carries: it is
// the cursor position report, and it is also the CSI spelling of F3. A recorder
// that reads a position report as a function key writes a tape that types F3 at
// the program under test.
//
// The rule is that a function key in the SS3 family reaches the CSI form only
// with explicit parameters, because its unmodified spelling is SS3.
func TestCursorPositionReportIsNotF3(t *testing.T) {
	notKeys := []string{
		"\x1b[R",      // cursor position report, no parameters
		"\x1b[12;40R", // cursor position report with a position
		"\x1b[S",      // the same collision on S
		"\x1b[2;3S",   // and with parameters that are not "1;mod"
	}
	for _, in := range notKeys {
		for _, c := range decodeCommands([]byte(in)) {
			if c.Kind == KindKey || c.Kind == KindType {
				t.Errorf("%q decoded as keyboard input %v", in, c.Keys)
			}
		}
	}

	// The genuine CSI spellings of the key still work.
	for in, want := range map[string]string{
		"\x1bOR":    "F3",
		"\x1b[1;5R": "Ctrl+F3",
		"\x1b[1;2S": "Shift+F4",
	} {
		c := decodeOne(t, in, Modes{})
		if c.Kind != KindKey || c.Keys[0] != want {
			t.Errorf("%q = %v %v, want Key %s", in, c.Kind, c.Keys, want)
		}
	}
}

// TestPlayerSendsKeyAttributes checks that the player replays what the tape
// says, attributes included. Decoding a release event and writing it to a file
// is pointless if replaying it sends a plain press instead.
//
// It works on the encoding the player uses rather than driving a terminal,
// which is the part that regressed: the player used to resolve each token on
// its own and had nowhere to put the attributes.
func TestPlayerSendsKeyAttributes(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{
			name: "release event",
			cmd:  Command{Kind: KindKey, Keys: []string{"a"}, KeyAttrs: KeyAttrs{Event: KeyRelease}},
			want: "\x1b[97;1:3u",
		},
		{
			name: "repeat with modifier",
			cmd:  Command{Kind: KindKey, Keys: []string{"Ctrl+a"}, KeyAttrs: KeyAttrs{Event: KeyRepeat}},
			want: "\x1b[97;5:2u",
		},
		{
			name: "associated text",
			cmd:  Command{Kind: KindKey, Keys: []string{"a"}, KeyAttrs: KeyAttrs{Text: "á"}},
			want: "\x1b[97;;225u",
		},
		{
			name: "plain chord keeps the legacy spelling",
			cmd:  Command{Kind: KindKey, Keys: []string{"Ctrl+a"}},
			want: "\x01",
		},
		{
			name: "multiple keys concatenate",
			cmd:  Command{Kind: KindKey, Keys: []string{"Ctrl+a", "Up", "Enter"}},
			want: "\x01\x1b[A\r",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeCommand(tc.cmd, Protocols())
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("player would send %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWhatSurvivesTheTapeFile pins exactly how much of the mode context a tape
// file carries, because the answer is not "all of it" and the difference
// matters when reading a recording made under kitty.
//
// A tape stores keys, not bytes. The mode context a key was decoded under lives
// in memory during recording but is not written to the file, so a plain chord
// comes back with no modes and replays in the legacy spelling. That is
// behaviourally equivalent: a program that negotiated kitty still accepts the
// legacy encoding.
//
// What must survive is everything the legacy encoding cannot express, since
// that is information rather than spelling. Those keys carry attributes, and
// attributes are written to the file and force the kitty encoding on replay.
func TestWhatSurvivesTheTapeFile(t *testing.T) {
	kitty := Modes{KittyFlags: 1}

	tests := []struct {
		name   string
		in     string
		tape   string
		replay string
	}{
		{
			name: "a plain chord reverts to the legacy spelling",
			in:   "\x1b[97;5u", tape: "Key Ctrl+a", replay: "\x01",
		},
		{
			name: "a release event survives exactly",
			in:   "\x1b[97;1:3u", tape: "Key a +Release", replay: "\x1b[97;1:3u",
		},
		{
			name: "associated text survives exactly",
			in:   "\x1b[97;;225u", tape: `Key a +Text "á"`, replay: "\x1b[97;;225u",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.TrimSpace(Sprint(decodeUnder([]byte(tc.in), kitty)))
			if src != tc.tape {
				t.Fatalf("tape line = %q, want %q", src, tc.tape)
			}

			back, err := Parse(strings.NewReader(src + "\n"))
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			out, err := encodeCommands(back)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(out) != tc.replay {
				t.Fatalf("replays as %q, want %q", out, tc.replay)
			}
		})
	}
}

// TestPlusIsAKeyName covers the one character that is both the modifier
// separator and a key. A token ending in '+' names '+' itself; what precedes it
// is the modifier list, which must carry its own separator.
func TestPlusIsAKeyName(t *testing.T) {
	ok := map[string]string{
		"+":     "+",     // the plus key, unmodified
		"Alt++": "\x1b+", // Alt and plus
	}
	for tok, want := range ok {
		got, err := ResolveKey(tok)
		if err != nil {
			t.Errorf("ResolveKey(%q): %v", tok, err)
			continue
		}
		if string(got) != want {
			t.Errorf("ResolveKey(%q) = %q, want %q", tok, got, want)
		}
	}

	// "Ctrl+" names a modifier with no key, and "C+" is the same mistake in
	// the short spelling. Both are rejected rather than silently read as the
	// plus key.
	for _, tok := range []string{"Ctrl+", "C+"} {
		if _, err := ResolveKey(tok); err == nil {
			t.Errorf("ResolveKey(%q) succeeded, want an error", tok)
		}
	}
}

// TestCtrlWithoutALegacyEncoding pins which Ctrl chords the legacy encoding can
// actually carry. Ctrl and '+' would send 0x0b, which reads back as Ctrl+k, so
// encoding it that way would silently change the key. It has no legacy
// spelling, and belongs to modifyOtherKeys or kitty.
func TestCtrlWithoutALegacyEncoding(t *testing.T) {
	if _, err := ResolveKey("Ctrl++"); err == nil {
		t.Error("Ctrl++ resolved under the legacy encoding, want an error")
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
