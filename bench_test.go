package tuitest

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// BenchmarkEmulatorPlainLines measures how fast the bundled VT interprets
// ordinary 80-column text lines.
func BenchmarkEmulatorPlainLines(b *testing.B) {
	line := strings.Repeat("x", 79) + "\r\n"
	chunk := []byte(strings.Repeat(line, 100))
	e := emu.New(80, 24)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Write(chunk)
	}
	b.ReportMetric(float64(b.N)*100/b.Elapsed().Seconds(), "lines/s")
}

// BenchmarkEmulatorStyledLines measures the same with an SGR change per line.
func BenchmarkEmulatorStyledLines(b *testing.B) {
	line := "\x1b[1;38;5;42m" + strings.Repeat("x", 79) + "\x1b[0m\r\n"
	chunk := []byte(strings.Repeat(line, 100))
	e := emu.New(80, 24)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Write(chunk)
	}
	b.ReportMetric(float64(b.N)*100/b.Elapsed().Seconds(), "lines/s")
}
