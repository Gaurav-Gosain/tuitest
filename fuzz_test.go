package tuitest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// FuzzDiff checks the golden-comparison diff on arbitrary inputs. Beyond "never
// panics", it asserts the property that makes a diff trustworthy: reading the
// context and removed lines back reproduces want exactly, and the context and
// added lines reproduce got. A diff that drops or duplicates a line would make
// a failing golden unreadable, and nothing else in the suite would notice.
func FuzzDiff(f *testing.F) {
	seeds := [][2]string{
		{"", ""},
		{"a", "a"},
		{"a\nb\nc", "a\nx\nc"},
		{"a\nb", ""},
		{"", "a\nb"},
		{"\n\n\n", "\n"},
		{"same\nsame", "same\nsame\nsame"},
		{"- a", "+ b"},
		{"  leading", "    leading"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, want, got string) {
		out := Diff(want, got)
		if out == "" && (want != "" || got != "") {
			// An empty diff is only correct for two empty inputs, which both
			// split to a single empty line and produce a context line.
			t.Fatalf("Diff produced no output for want %q got %q", want, got)
		}

		var minus, plus []string
		for _, line := range strings.Split(out, "\n") {
			if len(line) < 2 {
				t.Fatalf("diff line %q is shorter than its marker\nfull diff:\n%s", line, out)
			}
			body := line[2:]
			switch line[:2] {
			case "- ":
				minus = append(minus, body)
			case "+ ":
				plus = append(plus, body)
			case "  ":
				minus = append(minus, body)
				plus = append(plus, body)
			default:
				t.Fatalf("diff line %q has no recognised marker\nfull diff:\n%s", line, out)
			}
		}
		if rebuilt := strings.Join(minus, "\n"); rebuilt != want {
			t.Fatalf("context+removed lines do not reproduce want:\n got %q\nwant %q\ndiff:\n%s", rebuilt, want, out)
		}
		if rebuilt := strings.Join(plus, "\n"); rebuilt != got {
			t.Fatalf("context+added lines do not reproduce got:\n got %q\nwant %q\ndiff:\n%s", rebuilt, got, out)
		}
	})
}

// runLine matches an indented attribute run in the styled encoding.
var runLine = regexp.MustCompile(`^    (\d+)-(\d+) (\S+)$`)

// styledTokens are the attribute tokens the styled encoding is documented to
// emit. Colors are checked separately because they carry a value.
var styledTokens = map[string]bool{"b": true, "i": true, "u": true, "r": true, "s": true, "k": true, "w": true}

// FuzzStyledEncode drives the styled snapshot encoder with an arbitrary grid.
// It asserts that the encoding never panics, that every attribute run names
// columns inside the grid with start <= end and only documented tokens, that
// the plain rows are exactly Screen.Line, and that an unstyled screen degrades
// to the plain snapshot, which is the guarantee the README makes.
func FuzzStyledEncode(f *testing.F) {
	f.Add([]byte{1, 1, 'a', 0, 0})
	f.Add([]byte{4, 2, 'H', 0x01, 0, 'i', 0x01, 0, 'x', 0, 3, 'y', 0xff, 0xff})
	f.Add([]byte{2, 2, 'a', 0, 0, 'b', 0, 0, 'c', 0, 0, 'd', 0, 0})
	f.Add([]byte{})
	f.Add([]byte{0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		snap, styled := buildSnapshot(data)
		out := styledEncode(snap)

		if !styled && out != snap.Text() {
			t.Fatalf("unstyled screen did not degrade to the plain snapshot:\nstyled: %q\nplain:  %q", out, snap.Text())
		}
		if out == "" {
			return
		}

		row := -1
		for _, line := range strings.Split(out, "\n") {
			if m := runLine.FindStringSubmatch(line); m != nil && row >= 0 {
				checkRun(t, m, snap.cols, line)
				continue
			}
			row++
			if row >= snap.rows {
				t.Fatalf("encoding has more rows than the grid (%d):\n%s", snap.rows, out)
			}
			if line != snap.Line(row) {
				t.Fatalf("row %d text = %q, want %q\nfull encoding:\n%s", row, line, snap.Line(row), out)
			}
		}
	})
}

func checkRun(t *testing.T, m []string, cols int, line string) {
	t.Helper()
	start, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("run %q: start: %v", line, err)
	}
	end, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("run %q: end: %v", line, err)
	}
	if start < 0 || end < start || end >= cols {
		t.Fatalf("run %q has columns outside 0..%d", line, cols-1)
	}
	for _, tok := range strings.Split(m[3], ",") {
		switch {
		case styledTokens[tok]:
		case strings.HasPrefix(tok, "fg:"), strings.HasPrefix(tok, "bg:"):
			checkColorSpec(t, strings.TrimPrefix(strings.TrimPrefix(tok, "fg:"), "bg:"), line)
		default:
			t.Fatalf("run %q carries undocumented token %q", line, tok)
		}
	}
}

func checkColorSpec(t *testing.T, spec, line string) {
	t.Helper()
	if strings.HasPrefix(spec, "#") {
		if len(spec) != 7 {
			t.Fatalf("run %q: RGB spec %q is not #RRGGBB", line, spec)
		}
		if _, err := strconv.ParseUint(spec[1:], 16, 32); err != nil {
			t.Fatalf("run %q: RGB spec %q is not hex", line, spec)
		}
		return
	}
	n, err := strconv.Atoi(spec)
	if err != nil || n < 0 || n > 255 {
		t.Fatalf("run %q: indexed spec %q is not 0-255", line, spec)
	}
}

// buildSnapshot decodes fuzz bytes into a grid: two size bytes followed by
// three bytes per cell (rune, attribute bits, color). It reports whether any
// cell ended up carrying a non-default style. Runes come from a letters-only
// alphabet so a row's text can never be mistaken for an attribute run line.
func buildSnapshot(data []byte) (*screenSnapshot, bool) {
	const alphabet = "abcdefghij ABCDEFGHIJ" // no digits, no leading indent
	if len(data) < 2 {
		return &screenSnapshot{cols: 0, rows: 0}, false
	}
	cols := int(data[0]%16) + 1
	rows := int(data[1]%8) + 1
	data = data[2:]

	styled := false
	cells := make([][]Cell, rows)
	for row := range cells {
		cells[row] = make([]Cell, cols)
		for col := range cells[row] {
			c := Cell{Rune: ' ', Width: 1}
			if len(data) >= 3 {
				c.Rune = rune(alphabet[int(data[0])%len(alphabet)])
				attrs := data[1]
				c.Bold = attrs&0x01 != 0
				c.Italic = attrs&0x02 != 0
				c.Underline = attrs&0x04 != 0
				c.Reverse = attrs&0x08 != 0
				c.Strikethrough = attrs&0x10 != 0
				c.Blink = attrs&0x20 != 0
				if attrs&0x40 != 0 {
					c.Width = 2
				}
				if attrs&0x80 != 0 {
					c.Width = 0
				}
				switch data[2] % 3 {
				case 1:
					c.Fg = Color{Kind: ColorIndexed, Index: data[2]}
				case 2:
					c.Bg = Color{Kind: ColorRGB, R: data[2], G: data[1], B: data[0]}
				}
				data = data[3:]
			}
			if cellAttrs(c) != "" {
				styled = true
			}
			cells[row][col] = c
		}
	}
	return &screenSnapshot{cols: cols, rows: rows, cells: cells}, styled
}

// FuzzEmulatorScreen feeds arbitrary bytes to the emulator the way a program
// under test would, then exercises every screen accessor. Child output is the
// one input tuitest cannot validate, so a panic here would be a panic inside
// somebody's test run. It also asserts the grid never exposes a raw control
// character as a cell rune, which is what a mishandled escape sequence looks
// like from an assertion's point of view.
func FuzzEmulatorScreen(f *testing.F) {
	f.Add([]byte("hello world\r\n"))
	f.Add([]byte("\x1b[31mred\x1b[0m\r\n\x1b[1;1H\x1b[2J"))
	f.Add([]byte("\x1b[?1049h\x1b[10;10Hdeep\x1b[?1049l"))
	f.Add([]byte("\x1bPtmux;\x1b\\\x1b]133;A\x07prompt$ \x1b]133;D;1\x07"))
	f.Add([]byte("\xff\xfe\xfd\x00\x01\x02"))
	f.Add([]byte("wide: 世界 and combining: é\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		e := emu.New(20, 6)
		if _, err := e.Write(data); err != nil {
			t.Fatalf("emulator rejected input: %v", err)
		}
		e.Resize(12, 4)
		if _, err := e.Write(data); err != nil {
			t.Fatalf("emulator rejected input after resize: %v", err)
		}

		term := &Terminal{emu: e, exitCode: -1}
		snap := term.snapshotLocked()

		cols, rows := snap.Size()
		if len(strings.Split(snap.Text(), "\n")) > rows {
			t.Fatalf("Text() has more lines than the %d-row grid:\n%q", rows, snap.Text())
		}
		for row := -1; row <= rows; row++ {
			for _, r := range snap.Line(row) {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("row %d exposes control rune %#U:\n%q", row, r, snap.Line(row))
				}
			}
		}
		// Out-of-range access is documented to return the zero Cell.
		for _, p := range [][2]int{{-1, 0}, {0, -1}, {cols, 0}, {0, rows}} {
			if got := snap.Cell(p[0], p[1]); got != (Cell{}) {
				t.Fatalf("Cell(%d,%d) out of range returned %+v, want the zero Cell", p[0], p[1], got)
			}
		}
		styledEncode(snap)
	})
}
