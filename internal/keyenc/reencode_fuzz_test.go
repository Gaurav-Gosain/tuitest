package keyenc

import "testing"

// With no keyboard mode set, Reencode must send every input exactly as it was
// written. That is what keeps a program that negotiated nothing seeing the
// bytes it always saw.
func FuzzReencodeIsIdentityWithoutModes(f *testing.F) {
	for _, s := range []string{"\x02", "\x1b", "\x1b\x1b", "\x1b[A", "\x1b[1A", "\x1bOP", "\x1b[1;5A", "hi\r", "\x1b[?1u", "\x1b[", "\xff"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		if got := Reencode(in, Modes{}); string(got) != string(in) {
			t.Fatalf("Reencode(%q) = %q with no modes, want it unchanged", in, got)
		}
	})
}
