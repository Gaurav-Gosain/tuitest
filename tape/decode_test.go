package tape

import (
	"strings"
	"testing"
)

// decodeAll runs a chunk sequence through a decoder and returns the tape source
// it produced.
func decodeAll(chunks ...[]byte) string {
	var d inputDecoder
	var cmds []Command
	for _, c := range chunks {
		d.feed(c)
		cmds = append(cmds, d.take()...)
	}
	d.close()
	cmds = append(cmds, d.take()...)
	return strings.TrimRight(Sprint(cmds), "\n")
}

func TestDecodeInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text becomes one Type", "hello", "Type hello"},
		{"enter is a key", "\r", "Key Enter"},
		{"text then enter", "hi\r", "Type hi\nKey Enter"},
		{"consecutive keys coalesce", "\r\t", "Key Enter Tab"},
		{"arrow keys", "\x1b[A\x1b[B", "Key Up Down"},
		{"function key with parameters", "\x1b[15~", "Key F5"},
		{"ss3 function key", "\x1bOP", "Key F1"},
		{"control chord", "\x03", "Key Ctrl+c"},
		{"backspace", "\x7f", "Key Backspace"},
		{"bare escape is the esc key", "\x1b", "Key Esc"},
		{"escape then printable is alt", "\x1bx", "Key Alt+x"},
		{"tab is not decoded as ctrl+i", "\t", "Key Tab"},
		{"nul is ctrl+@", "\x00", "Key Ctrl+@"},
		{"high control byte", "\x1d", "Key Ctrl+]"},
		{"unicode text", "héllo", "Type héllo"},
		{"mixed run", "ab\x1b[Ccd\r", "Type ab\nKey Right\nType cd\nKey Enter"},
		{"leading space becomes a key", " hi", "Key Space\nType hi"},
		{"trailing space becomes a key", "hi ", "Type hi\nKey Space"},
		{"only spaces become keys", "  ", "Key Space Space"},
		{"interior spaces stay in the text", "a b", "Type a b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeAll([]byte(tc.input))
			if got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestDecodeSplitAcrossChunks covers a keypress arriving in pieces, which is
// what a slow pipe or a paste can do. The decoder must not emit the fragments as
// literal text.
func TestDecodeSplitAcrossChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"csi split after introducer", []string{"\x1b[", "A"}, "Key Up"},
		{"csi split mid parameter", []string{"\x1b[1", "5~"}, "Key F5"},
		{"ss3 split", []string{"\x1bO", "P"}, "Key F1"},
		{"utf8 rune split", []string{"h\xc3", "\xa9"}, "Type hé"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks := make([][]byte, len(tc.chunks))
			for i, c := range tc.chunks {
				chunks[i] = []byte(c)
			}
			if got := decodeAll(chunks...); got != tc.want {
				t.Errorf("decode %q:\n got: %q\nwant: %q", tc.chunks, got, tc.want)
			}
		})
	}
}

// TestDecodedKeysReplayToTheSameBytes is the property that makes a recording
// trustworthy: every key token the decoder emits must resolve back to exactly
// the bytes that produced it. A decoder that named Ctrl+j "Enter", or that got a
// control-byte mapping off by one, would replay different input than was
// recorded and this test would catch it.
func TestDecodedKeysReplayToTheSameBytes(t *testing.T) {
	inputs := []string{
		"\r", "\t", "\x7f", "\x1b",
		"\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D",
		"\x1b[H", "\x1b[F", "\x1b[2~", "\x1b[3~", "\x1b[5~", "\x1b[6~",
		"\x1bOP", "\x1bOQ", "\x1bOR", "\x1bOS",
		"\x1b[15~", "\x1b[17~", "\x1b[18~", "\x1b[19~",
		"\x1b[20~", "\x1b[21~", "\x1b[23~", "\x1b[24~",
		"\x00", "\x01", "\x02", "\x03", "\x0a", "\x1a",
		"\x1c", "\x1d", "\x1e", "\x1f",
		"\x1bx", "\x1bZ",
		" ",
	}

	for _, in := range inputs {
		var d inputDecoder
		d.feed([]byte(in))
		d.close()
		cmds := d.take()

		var replayed strings.Builder
		for _, c := range cmds {
			switch c.Kind {
			case KindKey:
				for _, tok := range c.Keys {
					k, err := ResolveKey(tok)
					if err != nil {
						t.Fatalf("input %q decoded to key %q which does not resolve: %v", in, tok, err)
					}
					replayed.WriteString(string(k))
				}
			case KindType:
				replayed.WriteString(c.Text)
			default:
				t.Fatalf("input %q produced unexpected command kind %d", in, c.Kind)
			}
		}
		if replayed.String() != in {
			t.Errorf("input %q replays as %q, not itself", in, replayed.String())
		}
	}
}

// TestDecodeDropsUnrepresentableSequences checks that a mouse report is consumed
// rather than leaking its bytes into a Type command, and that it is counted so
// the recorder can warn about it.
func TestDecodeDropsUnrepresentableSequences(t *testing.T) {
	var d inputDecoder
	d.feed([]byte("a\x1b[<0;10;5Mb"))
	d.close()
	cmds := d.take()

	got := strings.TrimRight(Sprint(cmds), "\n")
	want := "Type a\nType b"
	if got != want {
		t.Errorf("mouse report leaked into the tape:\n got: %q\nwant: %q", got, want)
	}
	if d.dropped != 1 {
		t.Errorf("dropped = %d, want 1", d.dropped)
	}
}
