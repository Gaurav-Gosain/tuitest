package tape

import "testing"

// The tests in this file are the evidence for the reported bug. Every one of
// them fails against the decoder as it was before the protocol registry landed,
// and each names the specific way that decoder was wrong.

// TestDecodeNeverDropsInput is the completeness-by-construction property stated
// as an example. Every sequence here had no tape representation before, so the
// old decoder consumed it and incremented a "dropped" counter, producing a tape
// that replays as silence. None of them may vanish.
func TestDecodeNeverDropsInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"sgr mouse press", "\x1b[<0;10;5M", "Mouse Press Left 9 4"},
		{"sgr mouse release", "\x1b[<0;10;5m", "Mouse Release Left 9 4"},
		{"x10 mouse press", "\x1b[M\x20\x2a\x25", "Mouse Press Left 9 4 +X10"},
		{"bracketed paste", "\x1b[200~hi\x1b[201~", `Paste "hi"`},
		{"focus in", "\x1b[I", "Focus In"},
		{"focus out", "\x1b[O", "Focus Out"},

		// The reported corruption: an APC reply to a kitty graphics query.
		// The old decoder shredded this into
		//   Key Alt+_ / Type Gi=1;OK / Key Alt+\
		// which replays as three bogus keystrokes.
		{"apc reply is not keystrokes", "\x1b_Gi=1;OK\x1b\\", `Raw "\x1b_Gi=1;OK\x1b\\"`},

		// Other reply classes that arrive on the input channel and must not be
		// decoded as typing.
		{"osc reply", "\x1b]11;rgb:0000/0000/0000\x1b\\", `Raw "\x1b]11;rgb:0000/0000/0000\x1b\\"`},
		{"dcs reply", "\x1bP>|tuios 1.0\x1b\\", `Raw "\x1bP>|tuios 1.0\x1b\\"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAll([]byte(tc.input)); got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestDecodeSurroundingTextIsPreserved checks that a captured sequence does not
// swallow the text on either side of it, which is how a dropped mouse report
// used to corrupt the surrounding Type commands.
func TestDecodeSurroundingTextIsPreserved(t *testing.T) {
	got := decodeAll([]byte("a\x1b[<0;10;5Mb"))
	want := "Type a\nMouse Press Left 9 4\nType b"
	if got != want {
		t.Errorf("decode:\n got: %q\nwant: %q", got, want)
	}
}

// TestDecodeDragReadsAsDrag covers the readability requirement: a drag must
// read as a line a human could have written, not a hex blob.
func TestDecodeDragReadsAsDrag(t *testing.T) {
	// SGR button 0 with the motion bit (32) set is a left-button drag.
	got := decodeAll([]byte("\x1b[<32;11;6M"))
	want := "Mouse Drag Left 10 5"
	if got != want {
		t.Errorf("decode:\n got: %q\nwant: %q", got, want)
	}
}

// TestStrandedStringTerminatorIsNotAKeystroke covers the reported defect in its
// smallest form. A control string closed by the 8-bit terminator leaves the
// 7-bit spelling ESC \ stranded in the stream, and reading that as Alt+\ is the
// same mistake as reading a whole APC reply as Alt+_ plus text: a terminal
// reply becomes input the user never typed.
//
// Verified to fail on broken code: removing '\\' from the control-string
// introducer list in legacyKeys.Decode decodes the stranded terminator as
// "Key Alt+\", and removing the '\\' case from frameEnd splits it into a bare
// Raw ESC followed by a literal backslash instead of one sequence.
func TestStrandedStringTerminatorIsNotAKeystroke(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		// The stranded terminator alone.
		{"\x1b\\", "Raw \"\\x1b\\\\\"\n"},
		// The shape the fuzzer found: an APC closed by the 8-bit terminator,
		// so the 7-bit one that follows belongs to nothing.
		{"\x1b_\x9c\x1b\\", "Raw \"\\x1b_\\x9c\"\nRaw \"\\x1b\\\\\"\n"},
		// A well formed reply is still one Raw, which is what stops this rule
		// from costing readability where it matters.
		{"\x1b_ok\x1b\\", "Raw \"\\x1b_ok\\x1b\\\\\"\n"},
	} {
		got := Sprint(decodeChunks(Modes{}, []byte(tc.in)))
		if got != tc.want {
			t.Errorf("decoding %q:\n  got:  %q\n  want: %q", tc.in, got, tc.want)
		}
		// Whatever the spelling, the bytes must survive.
		if replayed := replayBytes(t, decodeChunks(Modes{}, []byte(tc.in)), Modes{}); string(replayed) != tc.in {
			t.Errorf("decoding %q did not replay to itself: %q", tc.in, replayed)
		}
	}
}
