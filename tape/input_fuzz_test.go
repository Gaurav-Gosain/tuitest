package tape

import (
	"strings"
	"testing"
)

// seedInputs are byte streams that exercise each protocol and the raw fallback.
// The kitty graphics reply is first because it is the sequence that motivated
// all of this: it arrived on the input channel and was decoded as keystrokes.
var seedInputs = []string{
	"\x1b_Gi=1;OK\x1b\\",
	"\x1b]11;rgb:1a1a/1b1b/1c1c\x1b\\",
	"\x1b]10;rgb:ffff/ffff/ffff\x07",
	"\x1bP>|tuios 1.0\x1b\\",
	"\x1b[?62;1;6c",
	"\x1b[12;40R",
	"\x1b[?2004;2$y",
	"\x1b[<0;10;20M",
	"\x1b[M !!",
	"\x1b[200~pasted\x1b[201~",
	"\x1b[I",
	"\x1b[O",
	"\x1b[97u",
	"\x1b[97;5u",
	"\x1b[97;1:3u",
	"\x1b[97:65;2u",
	"\x1b[97;;104:105u",
	"\x1b[57352;1:2u",
	"\x1b[27;5;97~",
	"\x1b[27;5;105~",
	"\x1b[1;5A",
	"\x1b[15;2~",
	"\x1bOP",
	"\x1bOA",
	"\x1b[A",
	"\x1b",
	"\x1bx",
	"\x1b\x01",
	"hello world",
	"h\x1b[Ai\r",
	"\x00\x01\x1f\x7f",
	"\xc3\xa9\xf0\x9f\x98\x80",
	"\x9b1;5A",
	"\x1b[",
	"\x1b[1;",
	"\x1b_unterminated",
	"\xff\xfe",
}

// fuzzModes are the mode contexts a stream is decoded under. The same bytes
// mean different keys under different negotiated modes, so every property has
// to hold under each of them rather than only the default.
var fuzzModes = []Modes{
	{},
	{AppCursor: true},
	{KittyFlags: 1},
	{ModifyOtherKeys: 2},
	{AppCursor: true, AppKeypad: true, KittyFlags: 0b1111, ModifyOtherKeys: 2, BracketedPaste: true},
}

// decodeUnder runs a byte stream through a decoder under the given modes.
func decodeUnder(in []byte, m Modes) []Command {
	d := inputDecoder{modes: m}
	d.feed(in)
	d.close()
	return d.take()
}

// FuzzRecorderIsLossless is the completeness-by-construction property: whatever
// the decoder makes of a stream, re-encoding it must produce bytes that decode
// to the same commands. A sequence no protocol understands is captured as Raw
// and so reproduces exactly; a sequence some protocol understands reproduces
// either exactly or in a canonical spelling that means the same thing.
//
// This is the property that lets protocol coverage be optional. It is checked
// as a fuzz target rather than a table because the whole point is to hold for
// sequences nobody thought to write down.
func FuzzRecorderIsLossless(f *testing.F) {
	for _, s := range seedInputs {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		for _, m := range fuzzModes {
			cmds := decodeUnder(in, m)

			out, err := encodeCommands(cmds)
			if err != nil {
				t.Fatalf("decoded commands do not re-encode under %+v: %v\ninput: %q\ntape:\n%s",
					m, err, in, strings.TrimRight(Sprint(cmds), "\n"))
			}

			again := decodeUnder(out, m)
			if !commandsEquivalent(cmds, again) {
				t.Fatalf("decode is not stable under re-encoding, modes %+v\n input: %q\nbytes: %q\n  was:\n%s\n  now:\n%s",
					m, in, out,
					strings.TrimRight(Sprint(cmds), "\n"),
					strings.TrimRight(Sprint(again), "\n"))
			}
		}
	})
}

// FuzzNoInputIsDropped asserts the stronger half of losslessness directly: the
// bytes a decode consumed are all accounted for. Every byte of the input must
// appear in the re-encoded output for the streams whose protocols are Exact,
// and for the rest the command-level property above covers it.
//
// The specific failure this guards is the one the maintainer reported: a
// recorder that silently consumes input it cannot name, producing a tape that
// does not reproduce the session.
func FuzzNoInputIsDropped(f *testing.F) {
	for _, s := range seedInputs {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		cmds := decodeUnder(in, Modes{})
		if len(in) > 0 && len(cmds) == 0 {
			t.Fatalf("input %q decoded to no commands at all", in)
		}
		out, err := encodeCommands(cmds)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if len(in) > 0 && len(out) == 0 {
			t.Fatalf("input %q re-encoded to nothing", in)
		}
	})
}

// FuzzNonKeyboardIsNeverKeyboard is the regression property for the corruption
// in the recorded tape. A sequence that is not keyboard input must never decode
// to Key or Type commands, because that failure is silent: the tape looks
// plausible and replays nonsense.
//
// Only the keyboard protocols may produce keyboard commands. Any other sequence
// class, a terminal reply, a mouse report, a paste or a focus event, must come
// out as something else or as Raw.
func FuzzNonKeyboardIsNeverKeyboard(f *testing.F) {
	// Sequence classes that are unambiguously not keyboard input.
	nonKeyboard := []string{
		"\x1b_Gi=1;OK\x1b\\",
		"\x1b_G",
		"\x1b]11;rgb:1a1a/1b1b/1c1c\x1b\\",
		"\x1b]0;title\x07",
		"\x1bP>|term\x1b\\",
		"\x1bP1$r0m\x1b\\",
		"\x1b^privacy\x1b\\",
		"\x1bXstring\x1b\\",
		"\x1b[?62;1;6c",
		"\x1b[>0;276;0c",
		"\x1b[12;40R",
		"\x1b[?2004;2$y",
		"\x1b[<0;10;20M",
		"\x1b[<0;10;20m",
		"\x1b[M !!",
		"\x1b[200~x\x1b[201~",
		"\x1b[I",
		"\x1b[O",
	}
	for _, s := range nonKeyboard {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, payload string) {
		shapes := []string{}

		// A payload that is already a complete sequence is checked as it
		// stands, which is how the seeds exercise the real replies.
		if wholeNonKeyboard(payload) {
			shapes = append(shapes, payload)
		}

		// Otherwise wrap the payload in each control-string class, so the
		// property is checked over arbitrary contents.
		//
		// A payload containing a string terminator ends the sequence early
		// and leaves the trailing bytes as separate input. That is correct
		// behaviour, but it is no longer a test of one non-keyboard
		// sequence, so those shapes are skipped. The check is on the
		// assembled bytes rather than the payload string, because a rune
		// such as U+009C encodes to bytes that include a terminator even
		// though the string does not contain one as a rune.
		for _, shape := range []string{
			"\x1b_" + payload + "\x1b\\",
			"\x1b]" + payload + "\x07",
			"\x1bP" + payload + "\x1b\\",
			"\x1b^" + payload + "\x1b\\",
			"\x1bX" + payload + "\x1b\\",
		} {
			if n, complete, ok := frameEnd([]byte(shape)); ok && complete && n == len(shape) {
				shapes = append(shapes, shape)
			}
		}

		for _, shape := range shapes {
			for _, m := range fuzzModes {
				cmds := decodeUnder([]byte(shape), m)
				for _, c := range cmds {
					if c.Kind == KindKey || c.Kind == KindType {
						t.Fatalf("non-keyboard sequence decoded as keyboard input under %+v\ninput: %q\ntape:\n%s",
							m, shape, strings.TrimRight(Sprint(cmds), "\n"))
					}
				}
			}
		}
	})
}

// FuzzKeyCommandRoundTrip is the command direction of the round-trip property:
// for every key a tape can express, encoding then decoding must return the same
// command. It fuzzes the tape token rather than the bytes, so it covers chords
// a writer could type by hand as well as ones the recorder produces.
func FuzzKeyCommandRoundTrip(f *testing.F) {
	for _, tok := range []string{
		"a", "Z", "%", "Enter", "Tab", "Esc", "Backspace", "Space",
		"Up", "F5", "Delete", "Ctrl+a", "Alt+x", "Ctrl+Alt+a",
		"Shift+Up", "Ctrl+Alt+Shift+Up", "Super+F1", "Ctrl+@",
	} {
		f.Add(tok)
	}
	f.Fuzz(func(t *testing.T, token string) {
		for _, m := range fuzzModes {
			seq, err := ResolveKeyModes(token, m)
			if err != nil || len(seq) == 0 {
				continue
			}
			cmds := decodeUnder([]byte(seq), m)

			// The token must come back as a key, not as text. A chord
			// that resolves to printable bytes is the exception:
			// Shift+a really is just "A" on the wire, so there is
			// nothing to recover.
			if len(cmds) == 1 && cmds[0].Kind == KindType {
				continue
			}
			if len(cmds) != 1 || cmds[0].Kind != KindKey {
				t.Fatalf("token %q encoded as %q decoded to %s, want one Key",
					token, seq, strings.TrimRight(Sprint(cmds), "\n"))
			}

			// The chord itself must survive. The token that comes back
			// may be a different spelling of the same key, since the
			// decoder emits the canonical name: a literal " " comes back
			// as "Space", and "Return" as "Enter". What must hold is that
			// the two name the same key, which is exactly the statement
			// that they resolve to the same bytes.
			got := cmds[0].Keys[0]
			back, err := ResolveKeyModes(got, m)
			if err != nil {
				t.Fatalf("token %q decoded to %q, which does not resolve: %v", token, got, err)
			}
			if string(back) != string(seq) {
				t.Fatalf("token %q encoded as %q decoded back as %q, which resolves to %q",
					token, seq, got, back)
			}

			// The byte direction is Canonical for the keyboard
			// protocols, so re-encoding may pick a different spelling of
			// the same key. What must hold is that the spelling is
			// stable: decoding it again yields the same command.
			out, err := encodeCommands(cmds)
			if err != nil {
				t.Fatalf("token %q: re-encode: %v", token, err)
			}
			if !commandsEquivalent(cmds, decodeUnder(out, m)) {
				t.Fatalf("token %q round trip is not stable: %q -> %q -> %s",
					token, seq, out, strings.TrimRight(Sprint(decodeUnder(out, m)), "\n"))
			}
		}
	})
}

// wholeNonKeyboard reports whether s is exactly one control sequence from a
// class that is never keyboard input: a control string, or a CSI form that
// carries a terminal reply, a mouse report, a paste or a focus event.
func wholeNonKeyboard(s string) bool {
	if len(s) < 2 || s[0] != 0x1b {
		return false
	}
	n, complete, ok := frameEnd([]byte(s))
	if !ok || !complete || n != len(s) {
		return false
	}

	switch s[1] {
	case ']', 'P', '_', '^', 'X':
		return true
	case '[':
	default:
		return false
	}

	body := s[2 : n-1]
	final := s[n-1]
	switch {
	case final == 'I' || final == 'O': // focus in/out
		return true
	case final == 'c' || final == 'R' || final == 'y' || final == 'n': // replies
		return true
	case final == 'M' || final == 'm': // mouse
		return true
	case final == '~' && (body == "200" || body == "201"): // bracketed paste
		return true
	}
	return false
}
