package tape

import (
	"strings"
	"testing"
)

// TestDecodeIsIndependentOfChunkBoundaries is the property a recorder needs but
// the per-sequence split tests miss: a read boundary may fall anywhere, so
// decoding a stream in two pieces must agree with decoding it whole no matter
// where it is cut. The interesting cut is immediately after an ESC, where the
// decoder cannot yet tell the Esc key from the start of a longer sequence.
func TestDecodeIsIndependentOfChunkBoundaries(t *testing.T) {
	streams := []string{
		"\x1b[A", "\x1b[15~", "\x1bOP", "\x1b[3~",
		"a\x1b[Cb", "hi\r\x1b[B", "\x1b[A\x1b[B",
	}
	for _, s := range streams {
		whole := decodeAll([]byte(s))
		for i := 1; i < len(s); i++ {
			got := decodeAll([]byte(s[:i]), []byte(s[i:]))
			if got != whole {
				t.Errorf("stream %q split at %d:\n got: %q\nwant: %q", s, i, got, whole)
			}
		}
	}
}

// TestDecodeBoundsAnUnterminatedSequence covers a program (or a hostile paste)
// that opens a control sequence and never closes it. The decoder holds an
// incomplete sequence waiting for its final byte, so without a bound that hold
// grows for as long as the bytes keep coming.
func TestDecodeBoundsAnUnterminatedSequence(t *testing.T) {
	var d inputDecoder
	d.feed([]byte("\x1b["))
	for range 64 {
		d.feed([]byte(strings.Repeat("1", 4096)))
	}
	if len(d.pending) > maxPendingBytes {
		t.Errorf("held %d bytes waiting for a final byte, bound is %d", len(d.pending), maxPendingBytes)
	}
}

// TestUnterminatedSequenceIsKeptVerbatim is the completeness half of the bound:
// giving up on a sequence must not lose its bytes, because a tape that silently
// drops input is not a replay of the session. The overflowed hold is emitted as
// Raw, which the player writes through byte for byte.
func TestUnterminatedSequenceIsKeptVerbatim(t *testing.T) {
	var d inputDecoder
	payload := "\x1b[" + strings.Repeat("1", maxPendingBytes*2)
	d.feed([]byte(payload))
	d.close()

	var got strings.Builder
	for _, c := range d.take() {
		if c.Kind != KindRaw {
			t.Fatalf("unterminated sequence became %s, want Raw", c.Kind)
		}
		got.WriteString(c.Text)
	}
	if got.String() != payload {
		t.Errorf("Raw payload lost bytes: got %d of %d", got.Len(), len(payload))
	}
}
