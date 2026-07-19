package tape

import (
	"strings"
	"testing"
)

// decodeCommands runs chunks through a decoder and returns the commands, which
// is what the lossless and non-keyboard properties inspect. decodeAll in
// decode_test.go renders to source instead, which is the readable form.
func decodeCommands(chunks ...[]byte) []Command {
	var d inputDecoder
	var cmds []Command
	for _, c := range chunks {
		d.feed(c)
		cmds = append(cmds, d.take()...)
	}
	d.close()
	return append(cmds, d.take()...)
}

// TestDecodeAPCReplyIsNotKeystrokes is the regression for the corruption the
// maintainer recorded against tuios. A kitty graphics capability reply,
// ESC _ Gi=1;OK ESC \, arrived on the input channel and the decoder shredded it
// into "Key Alt+_", "Type Gi=1;OK" and "Key Alt+\". Replaying that sends three
// pieces of nonsense to the program instead of one capability reply.
//
// The property under test is not that APC decodes to any particular verb but
// that it never decodes to keyboard input, which is the failure that is silent.
func TestDecodeAPCReplyIsNotKeystrokes(t *testing.T) {
	const apc = "\x1b_Gi=1;OK\x1b\\"

	for _, c := range decodeCommands([]byte(apc)) {
		if c.Kind == KindKey || c.Kind == KindType {
			t.Fatalf("APC reply decoded as keyboard input %s, want a non-keyboard command\ngot tape:\n%s",
				c.Kind, strings.TrimRight(Sprint(decodeCommands([]byte(apc))), "\n"))
		}
	}
}
