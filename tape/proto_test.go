package tape

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest"
)

// TestMouseEncodingsDecode covers each wire encoding across the button,
// modifier and coordinate packing, including the cases the older encodings
// handle badly.
func TestMouseEncodingsDecode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// SGR (1006). The only encoding that names the button on release.
		{"sgr press left", "\x1b[<0;1;1M", "Mouse Press Left 0 0"},
		{"sgr press middle", "\x1b[<1;1;1M", "Mouse Press Middle 0 0"},
		{"sgr press right", "\x1b[<2;1;1M", "Mouse Press Right 0 0"},
		{"sgr release names its button", "\x1b[<2;1;1m", "Mouse Release Right 0 0"},
		{"sgr wheel up", "\x1b[<64;1;1M", "Mouse Press WheelUp 0 0"},
		{"sgr wheel down", "\x1b[<65;1;1M", "Mouse Press WheelDown 0 0"},
		{"sgr wheel left", "\x1b[<66;1;1M", "Mouse Press WheelLeft 0 0"},
		{"sgr wheel right", "\x1b[<67;1;1M", "Mouse Press WheelRight 0 0"},
		{"sgr back button", "\x1b[<128;1;1M", "Mouse Press Back 0 0"},
		{"sgr forward button", "\x1b[<129;1;1M", "Mouse Press Forward 0 0"},
		{"sgr move with nothing held", "\x1b[<35;1;1M", "Mouse Move None 0 0"},
		{"sgr drag names its button", "\x1b[<32;1;1M", "Mouse Drag Left 0 0"},
		{"sgr shift", "\x1b[<4;1;1M", "Mouse Press Left 0 0 +Shift"},
		{"sgr alt", "\x1b[<8;1;1M", "Mouse Press Left 0 0 +Alt"},
		{"sgr ctrl", "\x1b[<16;1;1M", "Mouse Press Left 0 0 +Ctrl"},
		{"sgr all modifiers", "\x1b[<28;1;1M", "Mouse Press Left 0 0 +Ctrl +Alt +Shift"},
		{"sgr wide coordinates", "\x1b[<0;500;300M", "Mouse Press Left 499 299"},

		// X10 (modes 9, 1000, 1002, 1003). Fields are single bytes.
		{"x10 press left", "\x1b[M\x20\x21\x21", "Mouse Press Left 0 0 +X10"},
		{"x10 press right", "\x1b[M\x22\x21\x21", "Mouse Press Right 0 0 +X10"},
		{"x10 release loses the button", "\x1b[M\x23\x21\x21", "Mouse Release None 0 0 +X10"},
		{"x10 drag", "\x1b[M\x40\x21\x21", "Mouse Drag Left 0 0 +X10"},
		{"x10 move", "\x1b[M\x43\x21\x21", "Mouse Move None 0 0 +X10"},
		{"x10 ctrl", "\x1b[M\x30\x21\x21", "Mouse Press Left 0 0 +Ctrl +X10"},
		// The largest coordinate the byte packing can carry.
		{"x10 at its coordinate limit", "\x1b[M\x20\xff\xff", "Mouse Press Left 222 222 +X10"},

		// urxvt (1015). X10's packing as decimal parameters.
		{"urxvt press left", "\x1b[32;1;1M", "Mouse Press Left 0 0 +Urxvt"},
		{"urxvt release loses the button", "\x1b[35;1;1M", "Mouse Release None 0 0 +Urxvt"},
		{"urxvt beyond the x10 limit", "\x1b[32;500;300M", "Mouse Press Left 499 299 +Urxvt"},
		{"urxvt wheel", "\x1b[96;1;1M", "Mouse Press WheelUp 0 0 +Urxvt"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAll([]byte(tc.input)); got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestMouseWireRoundTrip checks the byte direction for every encoding: the
// bytes a terminal sent must come back out of the tape unchanged.
func TestMouseWireRoundTrip(t *testing.T) {
	inputs := []string{
		"\x1b[<0;1;1M", "\x1b[<2;1;1m", "\x1b[<64;80;24M", "\x1b[<32;11;6M",
		"\x1b[<35;1;1M", "\x1b[<28;7;8M", "\x1b[<129;1;1M",
		"\x1b[M\x20\x21\x21", "\x1b[M\x23\x21\x21", "\x1b[M\x40\x21\x21",
		"\x1b[M\x20\xff\xff",
		"\x1b[32;1;1M", "\x1b[35;1;1M", "\x1b[32;500;300M",
	}
	for _, in := range inputs {
		cmds := decodeChunks(Modes{}, []byte(in))
		if len(cmds) != 1 || cmds[0].Kind != KindMouse {
			t.Errorf("%q decoded to %s, want one Mouse", in, strings.TrimRight(Sprint(cmds), "\n"))
			continue
		}
		got, ok := encodeCommand(cmds[0], Modes{})
		if !ok {
			t.Errorf("%q decoded to a command that cannot be encoded: %s", in, cmds[0])
			continue
		}
		if string(got) != in {
			t.Errorf("mouse wire round trip changed the bytes:\n  in:  %q\n  out: %q", in, got)
		}
	}
}

// TestMouseUnrepresentableFallsBackToRaw covers the cases the older encodings
// cannot express. Each must be captured verbatim rather than approximated,
// because a report that says the wrong button is worse than one the tape is
// honest about not understanding.
func TestMouseUnrepresentableFallsBackToRaw(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// A redundant leading zero is not a spelling any encoder produces, so
		// decoding it semantically would lose the original bytes.
		{"leading zero parameter", "\x1b[<0;01;1M", `Raw "\x1b[<0;01;1M"`},
		// Buttons 10 and 11 have no name in this model.
		{"unnamed high button", "\x1b[<130;1;1M", `Raw "\x1b[<130;1;1M"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAll([]byte(tc.input)); got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestUTF8MouseIsReadAsX10ButStillReplays documents the one ambiguity this
// track does not resolve, and shows why it does not matter.
//
// A mode 1005 report is UTF-8 encoded, and any three bytes that form one are
// also a syntactically valid X10 report, so the two are indistinguishable by
// construction. X10 wins, which means a 1005 report is recorded with the wrong
// coordinates in its Mouse line.
//
// What saves the recording is that the property the harness depends on is
// losslessness, not semantic accuracy: the bytes still replay exactly, so the
// program under test receives the report it originally received. This is the
// case that shows why the raw fallback is the correctness mechanism and
// semantic decoding is the readability one.
func TestUTF8MouseIsReadAsX10ButStillReplays(t *testing.T) {
	// A 1005 report for a column past the X10 limit: 0xc4 0x80 is U+0100.
	const in = "\x1b[M\x20\xc4\x80\x21"

	cmds := decodeChunks(Modes{}, []byte(in))
	got := replayBytes(t, cmds, Modes{})
	if string(got) != in {
		t.Fatalf("a 1005 report did not replay as itself:\n  in:  %q\n  out: %q", in, got)
	}
}

// TestSGRPixelNeedsItsMode is the mode-context case: the bytes of a pixel
// report are identical to a cell report, so the only thing that can tell them
// apart is whether the program asked for mode 1016.
func TestSGRPixelNeedsItsMode(t *testing.T) {
	const wire = "\x1b[<0;101;51M"

	cells := decodeChunks(Modes{}, []byte(wire))
	if got := strings.TrimRight(Sprint(cells), "\n"); got != "Mouse Press Left 100 50" {
		t.Errorf("without mode 1016 the report should read as cells, got %q", got)
	}

	pixels := decodeChunks(ModeSet(map[int]bool{1016: true}), []byte(wire))
	if got := strings.TrimRight(Sprint(pixels), "\n"); got != "Mouse Press Left 100 50 +Pixel" {
		t.Errorf("with mode 1016 the report should read as pixels, got %q", got)
	}
}

// TestPasteDecoding covers bracketed paste, including the payload shapes that
// a naive reader would mangle.
func TestPasteDecoding(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "\x1b[200~hello\x1b[201~", `Paste "hello"`},
		{"empty", "\x1b[200~\x1b[201~", `Paste ""`},
		{"multiline", "\x1b[200~one\ntwo\x1b[201~", `Paste "one\ntwo"`},
		{"payload holding escapes", "\x1b[200~\x1b[Ax\x1b[201~", `Paste "\x1b[Ax"`},
		{"text around it", "a\x1b[200~p\x1b[201~b", "Type a\n" + `Paste "p"` + "\nType b"},
		// A paste whose payload contains the end marker: the first marker ends
		// the paste, and the rest is separate input. That is what a terminal
		// does and what stops a payload from injecting typed input.
		{
			"payload containing the end marker",
			"\x1b[200~evil\x1b[201~rm -rf\x1b[201~",
			`Paste "evil"` + "\nType rm -rf\n" + `Raw "\x1b[201~"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAll([]byte(tc.input)); got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestPasteInjectionIsRejectedInSource is the other half of the injection
// guard. A payload containing an end marker cannot be sent as a paste, so a
// hand-written tape asking for it is a parse error rather than a sequence that
// would silently deliver typed input to the program.
func TestPasteInjectionIsRejectedInSource(t *testing.T) {
	_, err := Parse(strings.NewReader("Paste \"evil\\x1b[201~rm -rf\"\n"))
	if err == nil {
		t.Fatal("a paste payload containing the end marker was accepted")
	}
	if !strings.Contains(err.Error(), "bracketed-paste marker") {
		t.Errorf("error does not explain the problem: %v", err)
	}
	if !strings.Contains(err.Error(), "Raw") {
		t.Errorf("error does not point at the alternative: %v", err)
	}
}

// TestUnterminatedPasteIsCapturedNotDropped covers a paste that the stream ends
// in the middle of. The bytes still have to survive.
func TestUnterminatedPasteIsCapturedNotDropped(t *testing.T) {
	const in = "\x1b[200~half a paste"
	got := decodeAll([]byte(in))
	want := `Raw "\x1b[200~half a paste"`
	if got != want {
		t.Errorf("decode %q:\n got: %q\nwant: %q", in, got, want)
	}
}

// TestFocusDecoding covers the focus protocol, and the surrounding text, since
// CSI I and CSI O are short enough to be easy to confuse with a key.
func TestFocusDecoding(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"focus in", "\x1b[I", "Focus In"},
		{"focus out", "\x1b[O", "Focus Out"},
		{"focus around typing", "a\x1b[Ob\x1b[Ic", "Type a\nFocus Out\nType b\nFocus In\nType c"},
		{"focus out is not the ss3 introducer", "\x1bOP", "Key F1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAll([]byte(tc.input)); got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestTerminalRepliesAreCapturedVerbatim is the reported defect, stated as a
// table. Every one of these arrives on the input channel as the answer to a
// query the program under test made, and every one of them was previously
// decoded as keystrokes.
func TestTerminalRepliesAreCapturedVerbatim(t *testing.T) {
	replies := []string{
		"\x1b_Gi=1;OK\x1b\\",               // kitty graphics
		"\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\", // OSC 11 background colour
		"\x1b]10;rgb:ffff/ffff/ffff\x07",   // OSC 10, BEL terminated
		"\x1bP>|WezTerm 20240203\x1b\\",    // XTVERSION
		"\x1bP1$r0 q\x1b\\",                // DECRQSS
		"\x1b[?62;1;2;6;9;15;22c",          // primary device attributes
		"\x1b[>0;276;0c",                   // secondary device attributes
		"\x1b[12;40R",                      // cursor position report
		"\x1b[?2004;1$y",                   // DECRPM
		"\x1b[?1016;2$y",                   // DECRPM for pixel mouse
		"\x1b_Gi=31,s=1,v=1;OK\x1b\\",      // kitty graphics with payload
		"\x1b^some private message\x1b\\",  // PM
	}

	for _, in := range replies {
		cmds := decodeChunks(Modes{}, []byte(in))
		if len(cmds) != 1 {
			t.Errorf("%q decoded to %d commands, want 1:\n%s",
				in, len(cmds), strings.TrimRight(Sprint(cmds), "\n"))
			continue
		}
		if cmds[0].Kind == KindKey || cmds[0].Kind == KindType {
			t.Errorf("%q decoded as keyboard input: %s", in, cmds[0])
			continue
		}
		if got, ok := encodeCommand(cmds[0], Modes{}); ok && string(got) != in {
			t.Errorf("%q does not replay as itself, got %q", in, got)
		} else if cmds[0].Kind == KindRaw && cmds[0].Text != in {
			t.Errorf("%q was not captured verbatim, got %q", in, cmds[0].Text)
		}
	}
}

// TestProtocolRegistryIsSelfConsistent checks the invariants the registry
// relies on, so a protocol added later cannot quietly break the decoder.
func TestProtocolRegistryIsSelfConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range registered {
		if p.Name() == "" {
			t.Errorf("protocol %T has an empty name", p)
		}
		if seen[p.Name()] {
			t.Errorf("protocol %q registered twice", p.Name())
		}
		seen[p.Name()] = true

		// A protocol must not claim input it was not given.
		if n, _, r := p.Decode(nil, Modes{}); r == Full || n != 0 {
			t.Errorf("protocol %q matched an empty buffer", p.Name())
		}
		// A protocol must decline commands it does not own.
		if _, ok := p.Encode(Command{Kind: KindSpawn, Argv: []string{"x"}}, Modes{}); ok {
			t.Errorf("protocol %q claimed to encode a Spawn command", p.Name())
		}
	}
	if len(registered) == 0 {
		t.Fatal("no protocols registered")
	}
}

// TestEveryMouseNameRoundTrips keeps the parse tables and the printer in step.
// A button or action that prints as a name the parser does not accept would
// produce recordings that do not load.
func TestEveryMouseNameRoundTrips(t *testing.T) {
	for name, btn := range mouseButtons {
		if got := mouseButtonNames[btn]; got != name {
			t.Errorf("button %q prints as %q", name, got)
		}
	}
	for name, act := range mouseActions {
		if got := mouseActionNames[act]; got != name {
			t.Errorf("action %q prints as %q", name, got)
		}
	}
	// Every button and action the decoder can produce must have a name.
	for b := tuitest.MouseLeft; b <= tuitest.MouseNone; b++ {
		if mouseButtonNames[b] == "" {
			t.Errorf("mouse button %d has no tape name", b)
		}
	}
	for a := tuitest.MousePress; a <= tuitest.MouseDrag; a++ {
		if mouseActionNames[a] == "" {
			t.Errorf("mouse action %d has no tape name", a)
		}
	}
}
