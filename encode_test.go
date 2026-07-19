package tuitest

import (
	"os"
	"strings"
	"testing"
)

func TestCtrl(t *testing.T) {
	if got := Ctrl('b'); got != "\x02" {
		t.Errorf("Ctrl('b') = %q, want \\x02", got)
	}
	if got := Ctrl('B'); got != "\x02" {
		t.Errorf("Ctrl('B') = %q, want \\x02", got)
	}
	if got := Ctrl('c'); got != "\x03" {
		t.Errorf("Ctrl('c') = %q, want \\x03", got)
	}
}

func TestAlt(t *testing.T) {
	if got := Alt("x"); got != "\x1bx" {
		t.Errorf("Alt(\"x\") = %q, want ESC x", got)
	}
	if got := Alt(Enter); got != "\x1b\r" {
		t.Errorf("Alt(Enter) = %q", got)
	}
}

func TestKeyString(t *testing.T) {
	got, err := keyString([]any{"ab", 'c', Enter})
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc\r" {
		t.Errorf("keyString = %q, want %q", got, "abc\r")
	}
	if _, err := keyString(42); err == nil {
		t.Error("expected error for unsupported type int")
	}
}

func TestColorSpec(t *testing.T) {
	cases := []struct {
		c    Color
		want string
	}{
		{Color{Kind: ColorDefault}, "default"},
		{Color{Kind: ColorIndexed, Index: 4}, "4"},
		{Color{Kind: ColorRGB, R: 0x11, G: 0x22, B: 0x33}, "#112233"},
	}
	for _, tc := range cases {
		if got := colorSpec(tc.c); got != tc.want {
			t.Errorf("colorSpec(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestMouseEncodeSGR(t *testing.T) {
	ev := MouseEvent{Col: 4, Row: 9, Button: MouseLeft, Action: MousePress}
	if got, ok := ev.EncodeSGR(); !ok || got != "\x1b[<0;5;10M" {
		t.Errorf("press = %q, %v", got, ok)
	}
	rel := MouseEvent{Col: 4, Row: 9, Button: MouseLeft, Action: MouseRelease}
	if got, ok := rel.EncodeSGR(); !ok || got != "\x1b[<0;5;10m" {
		t.Errorf("release = %q, %v", got, ok)
	}
	wheel := MouseEvent{Col: 0, Row: 0, Button: MouseWheelUp, Action: MousePress, Mods: ModCtrl}
	if got, ok := wheel.EncodeSGR(); !ok || got != "\x1b[<80;1;1M" {
		t.Errorf("wheel+ctrl = %q, %v, want \\x1b[<80;1;1M", got, ok)
	}
	drag := MouseEvent{Col: 4, Row: 9, Button: MouseLeft, Action: MouseDrag}
	if got, ok := drag.EncodeSGR(); !ok || got != "\x1b[<32;5;10M" {
		t.Errorf("drag = %q, %v, want \\x1b[<32;5;10M", got, ok)
	}
	move := MouseEvent{Col: 4, Row: 9, Button: MouseNone, Action: MouseMove}
	if got, ok := move.EncodeSGR(); !ok || got != "\x1b[<35;5;10M" {
		t.Errorf("move = %q, %v, want \\x1b[<35;5;10M", got, ok)
	}
}

func TestBuildEnvHermetic(t *testing.T) {
	// Set the variable the hermetic environment must not carry. Asserting the
	// absence of something nothing ever set passes whether or not the hermetic
	// default works, which is how a broken build could keep this test green.
	t.Setenv("TUIOS_SESSION", "leaked-from-the-parent")
	c := defaultConfig()
	c.term = "dumb"
	c.trueColor = true
	c.env = []string{"FOO=bar", "TERM=override"}
	env := c.buildEnv()
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["TERM"] != "override" {
		t.Errorf("TERM = %q, want override (WithEnv should win)", m["TERM"])
	}
	if m["COLORTERM"] != "truecolor" {
		t.Errorf("COLORTERM = %q", m["COLORTERM"])
	}
	if m["FOO"] != "bar" {
		t.Errorf("FOO = %q", m["FOO"])
	}
	if _, ok := m["TUIOS_SESSION"]; ok {
		t.Error("hermetic env must not carry TUIOS_SESSION")
	}
}

func TestBuildEnvInherit(t *testing.T) {
	t.Setenv("TUITEST_MARKER", "present")
	c := defaultConfig()
	c.inheritEnv = true
	env := c.buildEnv()
	found := false
	for _, kv := range env {
		if kv == "TUITEST_MARKER=present" {
			found = true
		}
	}
	if !found {
		t.Error("WithInheritEnv should carry parent env")
	}
	_ = os.Environ
}

func TestStyledEncode(t *testing.T) {
	// Two rows: row 0 has a bold span at cols 0-2, row 1 is plain.
	snap := &screenSnapshot{
		cols: 5, rows: 2,
		cells: [][]Cell{
			{
				{Rune: 'H', Width: 1, Bold: true},
				{Rune: 'i', Width: 1, Bold: true},
				{Rune: '!', Width: 1, Bold: true},
				{Rune: ' ', Width: 1},
				{Rune: ' ', Width: 1},
			},
			{
				{Rune: 'o', Width: 1}, {Rune: 'k', Width: 1},
				{Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}, {Rune: ' ', Width: 1},
			},
		},
	}
	got := styledEncode(snap)
	want := "Hi!\n    0-2 b\nok"
	if got != want {
		t.Errorf("styledEncode =\n%q\nwant\n%q", got, want)
	}
}

// Concealed text (SGR 8) is drawn blank by a real terminal, so the plain
// screen must not report it. Reporting it lets WaitForText match a string no
// user can see, which is an assertion passing on a screen that does not exist.
// Verified to fail: dropping the Conceal branch from Line makes this report
// "secret" instead of blanks.
func TestConcealedCellsAreBlankInPlainText(t *testing.T) {
	snap := &screenSnapshot{
		cols: 6, rows: 1,
		cells: [][]Cell{{
			{Rune: 'o', Width: 1},
			{Rune: 'k', Width: 1},
			{Rune: 's', Width: 1, Conceal: true},
			{Rune: 'e', Width: 1, Conceal: true},
			{Rune: 'c', Width: 1, Conceal: true},
			{Rune: '!', Width: 1},
		}},
	}
	got := snap.Text()
	want := "ok   !"
	if got != want {
		t.Errorf("Text() = %q, want %q (concealed cells must render blank)", got, want)
	}
}

// Faint and conceal must reach the styled encoding, or a golden cannot tell a
// hidden or dimmed run from a normal one and blesses the difference away.
// Verified to fail: removing the Faint and Conceal tokens from cellAttrs makes
// both encodings collapse to the plain "abc".
func TestStyledEncodeDistinguishesFaintAndConceal(t *testing.T) {
	row := func(mut func(*Cell)) [][]Cell {
		cells := []Cell{
			{Rune: 'a', Width: 1}, {Rune: 'b', Width: 1}, {Rune: 'c', Width: 1},
		}
		for i := range cells {
			mut(&cells[i])
		}
		return [][]Cell{cells}
	}
	plain := styledEncode(&screenSnapshot{cols: 3, rows: 1, cells: row(func(*Cell) {})})
	faint := styledEncode(&screenSnapshot{cols: 3, rows: 1, cells: row(func(c *Cell) { c.Faint = true })})
	conceal := styledEncode(&screenSnapshot{cols: 3, rows: 1, cells: row(func(c *Cell) { c.Conceal = true })})

	if faint == plain {
		t.Errorf("faint encodes identically to plain (%q)", plain)
	}
	if conceal == plain {
		t.Errorf("conceal encodes identically to plain (%q)", plain)
	}
	if !strings.Contains(faint, "0-2 f") {
		t.Errorf("faint run missing from %q", faint)
	}
	if !strings.Contains(conceal, "0-2 c") {
		t.Errorf("conceal run missing from %q", conceal)
	}
}

func TestUnifiedDiff(t *testing.T) {
	got := unifiedDiff("a\nb\nc", "a\nx\nc")
	if !strings.Contains(got, "- b") || !strings.Contains(got, "+ x") {
		t.Errorf("diff missing change lines:\n%s", got)
	}
	if !strings.Contains(got, "  a") || !strings.Contains(got, "  c") {
		t.Errorf("diff missing context lines:\n%s", got)
	}
}

func TestScreenTextTrims(t *testing.T) {
	snap := &screenSnapshot{
		cols: 4, rows: 3,
		cells: [][]Cell{
			{{Rune: 'h', Width: 1}, {Rune: 'i', Width: 1}, {Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}},
			{{Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}},
			{{Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}, {Rune: ' ', Width: 1}},
		},
	}
	if got := snap.Text(); got != "hi" {
		t.Errorf("Text() = %q, want %q", got, "hi")
	}
}
