package tape

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest"
)

// replayBytes renders decoded commands back into the bytes a replay would send
// to the child. It is the encoding half of the round-trip property, and it goes
// through the same registry the player and the decoder use, so a protocol
// cannot pass the property by having a second, more forgiving encoder.
func replayBytes(t *testing.T, cmds []Command, m Modes) []byte {
	t.Helper()
	var out []byte
	for _, c := range cmds {
		switch c.Kind {
		case KindType:
			out = append(out, c.Text...)
		case KindRaw:
			out = append(out, c.Text...)
		case KindKey:
			for _, tok := range c.Keys {
				k, err := ResolveKey(tok)
				if err != nil {
					t.Fatalf("decoder emitted key %q which does not resolve: %v", tok, err)
				}
				out = append(out, k...)
			}
		default:
			b, ok := encodeCommand(c, m)
			if !ok {
				t.Fatalf("decoder emitted %s which no protocol can encode: %+v", c.Kind, c)
			}
			out = append(out, b...)
		}
	}
	return out
}

// decodeChunks feeds a byte stream to a fresh decoder and returns the commands.
func decodeChunks(m Modes, chunks ...[]byte) []Command {
	d := inputDecoder{modes: m}
	var cmds []Command
	for _, c := range chunks {
		d.feed(c)
		cmds = append(cmds, d.take()...)
	}
	d.close()
	return append(cmds, d.take()...)
}

// FuzzRecorderIsLossless is the completeness-by-construction property, stated as
// an executable claim: whatever bytes a session produces, decoding them and
// replaying the result sends the child exactly those bytes back.
//
// This is the property that makes protocol coverage a readability concern rather
// than a correctness one. It holds for input no protocol recognises, because the
// framer captures such input verbatim as Raw, and it would fail immediately if
// any decode path ever consumed bytes without representing them. The decoder it
// replaced had exactly such a path, and this target fails against it.
func FuzzRecorderIsLossless(f *testing.F) {
	seeds := []string{
		"hello", "\r\n\t", "\x1b", "\x1bx",
		"\x1b[A\x1b[B", "\x1b[15~",
		"\x1b[<0;10;5M", "\x1b[<32;11;6M", "\x1b[<0;10;5m",
		"\x1b[M\x20\x2a\x25", "\x1b[35;10;5M",
		"\x1b[200~pasted\x1b[201~", "\x1b[I", "\x1b[O",
		"\x1b_Gi=1;OK\x1b\\", "\x1b]11;rgb:1111/2222/3333\x1b\\",
		"\x1bP>|term\x1b\\", "\x1b[?62;1;6c", "\x1b[12;40R",
		"\x1b[200~unterminated", "\x1b[<", "\x1b[M\x20",
		"\x80\xbf\xfe\xff", "a\x1b[<0;1;1Mb",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		cmds := decodeChunks(Modes{}, in)
		got := replayBytes(t, cmds, Modes{})
		if string(got) != string(in) {
			t.Fatalf("decode then replay is lossy:\n  in:  %q\n  out: %q\n  cmds: %s",
				in, got, strings.TrimRight(Sprint(cmds), "\n"))
		}
	})
}

// FuzzDecodeNeverFabricatesKeystrokes is the regression property for the
// reported corruption, and it guards the more important of the two defects: a
// dropped sequence announces itself, but a sequence decoded as the wrong thing
// does not.
//
// The claim is deliberately narrow so that it is actually true. A well-formed
// control string, meaning an introducer, a body free of embedded controls, and
// a proper string terminator, is a terminal reply in its entirety. It must come
// back as exactly one Raw command holding exactly those bytes: no Key, no Type,
// and no splitting into pieces.
//
// The bodies are sanitized rather than used as the fuzzer supplies them,
// because an embedded ESC or C0 control genuinely aborts a control string, and
// the bytes after the abort genuinely are separate input. Asserting otherwise
// would be asserting something false about terminals.
//
// Against the decoder this replaced, ESC _ Gi=1;OK ESC \\ produced
// Key Alt+_, Type Gi=1;OK and Key Alt+\\, so this target fails on its first seed.
func FuzzDecodeNeverFabricatesKeystrokes(f *testing.F) {
	for _, s := range []string{
		"Gi=1;OK", "11;rgb:0000/0000/0000", ">|tuios 1.0", "status", "",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		clean := sanitizeControlStringBody(body)
		for _, intro := range []string{"\x1b_", "\x1b]", "\x1bP", "\x1b^", "\x1bX"} {
			in := intro + string(clean) + "\x1b\\"
			cmds := decodeChunks(Modes{}, []byte(in))
			if len(cmds) != 1 || cmds[0].Kind != KindRaw || cmds[0].Text != in {
				t.Fatalf("terminal reply not captured as a single verbatim Raw:\n  in:   %q\n  cmds: %s",
					in, strings.TrimRight(Sprint(cmds), "\n"))
			}
		}
	})
}

// sanitizeControlStringBody strips the bytes that terminate or abort a control
// string, so what remains is a body a terminal could actually have sent inside
// one.
func sanitizeControlStringBody(body []byte) []byte {
	out := make([]byte, 0, len(body))
	for _, b := range body {
		if b < 0x20 || b == 0x7f {
			continue
		}
		out = append(out, b)
	}
	return out
}

// FuzzDecodeSplitIsStable checks that where a chunk boundary falls cannot change
// what a stream decodes to. A terminal delivers input in whatever sizes the
// kernel chooses, so a decoder that got this wrong would record different tapes
// for the same session depending on timing, which is the least debuggable kind
// of flake.
func FuzzDecodeSplitIsStable(f *testing.F) {
	f.Add([]byte("\x1b[<0;10;5M"), 2)
	f.Add([]byte("\x1b[200~hi\x1b[201~"), 7)
	f.Add([]byte("\x1b_Gi=1;OK\x1b\\"), 4)
	f.Add([]byte("\x1b[M\x20\x2a\x25"), 3)

	f.Fuzz(func(t *testing.T, in []byte, at int) {
		if len(in) == 0 {
			return
		}
		at = ((at % len(in)) + len(in)) % len(in)

		whole := Sprint(decodeChunks(Modes{}, in))
		split := Sprint(decodeChunks(Modes{}, in[:at], in[at:]))
		if whole != split {
			t.Fatalf("chunk boundary at %d changed the decoding of %q:\n whole: %q\n split: %q",
				at, in, whole, split)
		}
	})
}

// FuzzMouseCommandRoundTrip is the command direction of the round-trip
// property for the encodings this track added: encoding an event and decoding
// the result must give back the same event.
func FuzzMouseCommandRoundTrip(f *testing.F) {
	f.Add(0, 0, 0, 0, 10, 5, false)
	f.Add(3, 0, 7, 1, 0, 0, false)
	f.Add(1, 3, 0, 2, 222, 222, false)

	f.Fuzz(func(t *testing.T, action, enc, button, mods, col, row int, pixel bool) {
		ev := tuitest.MouseEvent{
			Col:    clampInt(col, 0, MaxDimension-1),
			Row:    clampInt(row, 0, MaxDimension-1),
			Button: tuitest.MouseButton(clampInt(button, 0, int(tuitest.MouseNone))),
			Action: tuitest.MouseAction(clampInt(action, 0, int(tuitest.MouseDrag))),
			Mods:   tuitest.KeyMods(clampInt(mods, 0, 7)),
			Enc:    tuitest.MouseEncoding(clampInt(enc, 0, int(tuitest.MouseURXVT))),
			Pixel:  pixel,
		}
		// Move is a spelling of "motion with nothing held", so a button on a
		// Move is not part of the event. Canonicalize before comparing rather
		// than pretending the encoder preserves it.
		if ev.Action == tuitest.MouseMove {
			ev.Button = tuitest.MouseNone
		}
		if ev.Pixel && ev.Enc != tuitest.MouseSGR {
			// Only SGR has a pixel variant.
			return
		}

		cmd := Command{Kind: KindMouse, Mouse: ev}
		modes := Modes{}
		if ev.Pixel {
			modes = ModeSet(map[int]bool{1016: true})
		}

		wire, ok := encodeCommand(cmd, modes)
		if !ok {
			// The encoding genuinely cannot express this event, which is a
			// documented outcome, not a failure: such events fall back to Raw.
			return
		}

		got := decodeChunks(modes, wire)
		if len(got) != 1 || got[0].Kind != KindMouse {
			t.Fatalf("mouse event %+v encoded to %q which decoded to %s",
				ev, wire, strings.TrimRight(Sprint(got), "\n"))
		}
		if got[0].Mouse != ev {
			t.Fatalf("mouse round trip changed the event:\n  in:   %+v\n  wire: %q\n  out:  %+v",
				ev, wire, got[0].Mouse)
		}
	})
}

// FuzzTapeRoundTripsThroughSource closes the loop between the decoder and the
// tape file: whatever the decoder produces must survive being written out and
// parsed back. Without this, the decoder could emit a command the grammar
// cannot express and the recording would not even load.
func FuzzTapeRoundTripsThroughSource(f *testing.F) {
	for _, s := range []string{
		"\x1b[<0;10;5M", "\x1b[M\x20\x2a\x25", "\x1b[200~hi\x1b[201~",
		"\x1b[I", "\x1b[O", "\x1b_Gi=1;OK\x1b\\", "hello\r",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		cmds := decodeChunks(Modes{}, in)
		src := Sprint(cmds)
		back, err := Parse(strings.NewReader(src))
		if err != nil {
			t.Fatalf("decoder produced a tape that does not parse: %v\nsource: %q", err, src)
		}
		if len(back) != len(cmds) {
			t.Fatalf("round trip changed the command count: %d then %d\nsource: %q",
				len(cmds), len(back), src)
		}
		if got := replayBytes(t, back, Modes{}); string(got) != string(in) {
			t.Fatalf("tape round trip changed the replayed bytes:\n  in:  %q\n  out: %q\n  src: %q",
				in, got, src)
		}
	})
}

func clampInt(v, lo, hi int) int {
	if v < 0 {
		v = -v
	}
	if hi <= lo {
		return lo
	}
	return lo + v%(hi-lo+1)
}
