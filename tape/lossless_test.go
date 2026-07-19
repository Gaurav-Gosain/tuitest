package tape

import (
	"strings"
	"testing"
)

// TestDecodeIsLossless is completeness by construction: whatever the decoder
// does with a sequence, re-encoding the commands it produced must reproduce the
// original bytes. A sequence with no semantic decoder still has to survive as
// Raw, so a tape replays faithfully regardless of protocol coverage.
func TestDecodeIsLossless(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"apc kitty graphics reply", "\x1b_Gi=1;OK\x1b\\"},
		{"osc color query reply", "\x1b]11;rgb:1a1a/1b1b/1c1c\x1b\\"},
		{"osc bel terminated", "\x1b]10;rgb:ffff/ffff/ffff\x07"},
		{"dcs xtversion reply", "\x1bP>|tuios 1.0\x1b\\"},
		{"primary device attributes", "\x1b[?62;1;6c"},
		{"cursor position report", "\x1b[12;40R"},
		{"decrpm bracketed paste", "\x1b[?2004;2$y"},
		{"sgr mouse press", "\x1b[<0;10;20M"},
		{"sgr mouse release", "\x1b[<0;10;20m"},
		{"x10 mouse", "\x1b[M !!"},
		{"bracketed paste", "\x1b[200~hello\x1b[201~"},
		{"focus in", "\x1b[I"},
		{"focus out", "\x1b[O"},
		{"kitty key press", "\x1b[97;5u"},
		{"kitty key with text", "\x1b[97;;97u"},
		{"modify other keys", "\x1b[27;5;97~"},
		{"privacy message", "\x1b^payload\x1b\\"},
		{"start of string", "\x1bXpayload\x1b\\"},
		{"plain text", "hello"},
		{"text and enter", "hi\r"},
		{"arrow key", "\x1b[A"},
		{"alt rune", "\x1bx"},
		{"bare escape", "\x1b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmds := decodeCommands([]byte(tc.input))
			got, err := encodeCommands(cmds)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if string(got) != tc.input {
				t.Fatalf("decode/encode lost bytes\n input: %q\noutput: %q\n  tape:\n%s",
					tc.input, got, strings.TrimRight(Sprint(cmds), "\n"))
			}
		})
	}
}
