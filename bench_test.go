package tuitest

import (
	"strconv"
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

// BenchmarkEmulatorWideLines measures lines of double-width CJK text and emoji
// clusters, which take the grapheme path instead of the ASCII fast path.
func BenchmarkEmulatorWideLines(b *testing.B) {
	line := strings.Repeat("漢字", 18) + "👍🏽❤️ café\r\n"
	chunk := []byte(strings.Repeat(line, 100))
	e := emu.New(80, 24)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Write(chunk)
	}
	b.ReportMetric(float64(b.N)*100/b.Elapsed().Seconds(), "lines/s")
}

// BenchmarkEmulatorFullScreenRedraw measures the shape a full-screen program
// writes on the alternate screen: every row addressed with CUP, a colour
// change, text, and an erase to the end of the line.
func BenchmarkEmulatorFullScreenRedraw(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("\x1b[?1049h")
	for row := 1; row <= 24; row++ {
		sb.WriteString("\x1b[")
		sb.WriteString(strconv.Itoa(row))
		sb.WriteString(";1H\x1b[38;2;200;100;50m")
		sb.WriteString(strings.Repeat("status ", 8))
		sb.WriteString("\x1b[0m\x1b[K")
	}
	frame := []byte(sb.String())
	e := emu.New(80, 24)
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Write(frame)
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "frames/s")
}

// BenchmarkEmulatorReadGrid measures reading every cell back, which the
// harness does for each snapshot and each poll of a wait.
func BenchmarkEmulatorReadGrid(b *testing.B) {
	e := emu.New(80, 24)
	_, _ = e.Write([]byte(strings.Repeat("\x1b[1mbold\x1b[0m plain 漢字 ", 90)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for row := 0; row < 24; row++ {
			for col := 0; col < 80; col++ {
				if c := e.CellAt(col, row); c != nil {
					n += len(c.Content)
				}
			}
		}
		if n == 0 {
			b.Fatal("read an empty grid")
		}
	}
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

// fillScreen paints every cell of a 120x40 terminal with styled text, the size
// tuios's e2e suite runs at, so the benchmarks below read a realistic grid.
func fillScreen(b *testing.B) *Terminal {
	b.Helper()
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.mu.Lock()
	term.emu.Resize(120, 40)
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	for row := 0; row < 40; row++ {
		sb.WriteString("\x1b[1;38;5;42m")
		sb.WriteString(strings.Repeat("y", 60))
		sb.WriteString("\x1b[0m")
		sb.WriteString(strings.Repeat("z", 60))
	}
	_, _ = term.emu.Write([]byte(sb.String()))
	term.mu.Unlock()
	return term
}

// BenchmarkScreenTextUnchanged is what a polling helper pays per read of a
// screen that has not changed since the last one: the e2e suite reads Screen
// in loops every few tens of milliseconds, and every WaitForText re-reads it on
// each wakeup.
func BenchmarkScreenTextUnchanged(b *testing.B) {
	term := fillScreen(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = term.Screen().Text()
	}
}

// BenchmarkScreenTextAfterOutput is the other case: the program wrote since the
// last read, so the grid has to be copied again. It guards the cache from
// making the common changed-screen read slower.
func BenchmarkScreenTextAfterOutput(b *testing.B) {
	term := fillScreen(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.onData([]byte("\x1b[Hq"))
		_ = term.Screen().Text()
	}
}
