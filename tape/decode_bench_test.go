package tape

import (
	"bytes"
	"testing"
)

// BenchmarkDecodeEscapeInLargeChunk models a paste: an arrow key at the head of
// a chunk that still has a lot of text behind it. Matching the named-sequence
// table must not depend on how much is behind the sequence.
func BenchmarkDecodeEscapeInLargeChunk(b *testing.B) {
	chunk := append([]byte("\x1b[A"), bytes.Repeat([]byte("x"), 32*1024)...)
	b.ReportAllocs()
	for b.Loop() {
		var d inputDecoder
		d.feed(chunk)
		d.close()
	}
}
