package tuitest

import (
	"os"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// TestCtrl pins Ctrl to the xterm table. Masking every rune to five bits, as
// Ctrl used to, is right for letters and wrong for most other keys: it turned
// Ctrl('2') into 0x12 (Ctrl+R), Ctrl('?') into 0x1f and Ctrl('1') into 0x11
// (Ctrl+Q, which is XON), and it truncated any rune above 0xff to a byte.
func TestCtrl(t *testing.T) {
	cases := []struct {
		r    rune
		want Key
	}{
		{'b', "\x02"},
		{'B', "\x02"},
		{'c', "\x03"},
		{'a', "\x01"},
		{'z', "\x1a"},
		{'@', "\x00"},
		{' ', "\x00"},
		{'[', "\x1b"},
		{'\\', "\x1c"},
		{']', "\x1d"},
		{'^', "\x1e"},
		{'_', "\x1f"},
		{'2', "\x00"},
		{'3', "\x1b"},
		{'4', "\x1c"},
		{'5', "\x1d"},
		{'6', "\x1e"},
		{'7', "\x1f"},
		{'8', "\x7f"},
		{'?', "\x7f"},
		{'/', "\x1f"},
		{'~', "\x1e"},
		{'1', "1"},
		{'9', "9"},
		{'é', "é"},
		{'ł', "ł"},
	}
	for _, tc := range cases {
		if got := Ctrl(tc.r); got != tc.want {
			t.Errorf("Ctrl(%q) = %q, want %q", tc.r, got, tc.want)
		}
	}
}

// TestCursorKeysFollowDECCKM covers the cursor key mode. A terminal sends the
// arrow keys, Home and End as SS3 sequences once a program sets mode 1, and a
// program that set it matches its input against those. Sending the CSI form
// regardless, as SendKeys used to, is sending a key the program does not know.
func TestCursorKeysFollowDECCKM(t *testing.T) {
	normal := map[Key]string{Up: "\x1b[A", Down: "\x1b[B", Right: "\x1b[C", Left: "\x1b[D", Home: "\x1b[H", End: "\x1b[F"}
	app := map[Key]string{Up: "\x1bOA", Down: "\x1bOB", Right: "\x1bOC", Left: "\x1bOD", Home: "\x1bOH", End: "\x1bOF"}
	for k, want := range normal {
		if got, _ := keyString(k, false); got != want {
			t.Errorf("keyString(%q, false) = %q, want %q", k, got, want)
		}
		if got, _ := keyString(k, true); got != app[k] {
			t.Errorf("keyString(%q, true) = %q, want %q", k, got, app[k])
		}
	}
	// Other keys and literal text are not affected by the mode.
	for _, k := range []Key{PageUp, Delete, F1, Enter, Esc} {
		if got, _ := keyString(k, true); got != string(k) {
			t.Errorf("keyString(%q, true) = %q, want it unchanged", k, got)
		}
	}
	if got, _ := keyString("\x1b[A", true); got != "\x1b[A" {
		t.Errorf("a plain string must be sent literally, got %q", got)
	}
	if got, _ := keyString([]any{Up, []Key{Down}}, true); got != "\x1bOA\x1bOB" {
		t.Errorf("keys inside slices must follow the mode too, got %q", got)
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
	got, err := keyString([]any{"ab", 'c', Enter}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc\r" {
		t.Errorf("keyString = %q, want %q", got, "abc\r")
	}
	if _, err := keyString(42, false); err == nil {
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

// A cell can hold a whole grapheme cluster, and Line used to report only its
// first rune. A program that drew "café" with a combining acute came back as
// "cafe", so WaitForText missed a string plainly on screen and a golden
// recorded the accent as absent and then defended that reading forever.
// Verified to fail: rendering Cell.Rune instead of Cell.Content drops the
// accent and the second half of the emoji sequence.
func TestPlainTextRendersWholeClusters(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"combining acute", "café"},
		{"zero width joiner", "\U0001F469‍\U0001F4BB!"},
		{"skin tone modifier", "\U0001F44D\U0001F3FD!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := emu.New(20, 3)
			if _, err := e.Write([]byte(tc.in)); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := snapshotOf(e, 20, 3).Text(); got != tc.in {
				t.Errorf("Text() = %q, want %q", got, tc.in)
			}
		})
	}
}

// SGR 5 and SGR 6 are separate attributes but one visible effect. Only the slow
// one was reported, so text a program made blink with SGR 6 came back unstyled
// and a styled golden recorded it that way.
// Verified to fail: dropping AttrRapidBlink from toCell reports Blink false.
func TestRapidBlinkIsReportedAsBlink(t *testing.T) {
	e := emu.New(10, 2)
	if _, err := e.Write([]byte("\x1b[6mD")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !snapshotOf(e, 10, 2).Cell(0, 0).Blink {
		t.Error("SGR 6 cell does not report Blink")
	}
}

// Concealing a wide rune has to blank both of the columns it covers. One space
// per cell shortened the line, which moves every column after it and makes a
// golden of a concealed CJK line disagree with the same line unconcealed.
// Verified to fail: writing a single space for a concealed cell yields "X Y".
func TestConcealedWideRuneKeepsLineLength(t *testing.T) {
	snap := &screenSnapshot{
		cols: 4, rows: 1,
		cells: [][]Cell{{
			{Rune: 'X', Content: "X", Width: 1},
			{Rune: '中', Content: "中", Width: 2, Conceal: true},
			{Width: 0},
			{Rune: 'Y', Content: "Y", Width: 1},
		}},
	}
	if got, want := snap.Text(), "X  Y"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

// snapshotOf copies an emulator's grid into the immutable form the assertions
// run against, the same way Terminal.Screen does under its lock.
func snapshotOf(e emu.Emulator, cols, rows int) *screenSnapshot {
	cells := make([][]Cell, rows)
	for row := 0; row < rows; row++ {
		cells[row] = make([]Cell, cols)
		for col := 0; col < cols; col++ {
			cells[row][col] = toCell(e.CellAt(col, row))
		}
	}
	return &screenSnapshot{cols: cols, rows: rows, cells: cells}
}

// The legacy encodings are checked against the byte sequences xterm's ctlseqs
// document specifies rather than against tuitest's own decoder. A round trip
// through our reader agrees with any consistent mistake; these do not.
func TestMouseEncodeLegacyMatchesXtermSpec(t *testing.T) {
	// CSI M Cb Cx Cy, each field a byte: the button offset by 32 and the
	// one-based coordinates offset by 32 on top of that.
	for _, tc := range []struct {
		name string
		ev   MouseEvent
		want string
	}{
		{
			"left press at the origin",
			MouseEvent{Col: 0, Row: 0, Button: MouseLeft, Action: MousePress},
			"\x1b[M\x20\x21\x21",
		},
		{
			"middle press with shift",
			MouseEvent{Col: 1, Row: 2, Button: MouseMiddle, Action: MousePress, Mods: ModShift},
			"\x1b[M\x25\x22\x23",
		},
		{
			// Wheel notches are buttons 4 and 5, carried in bit 64.
			"wheel up",
			MouseEvent{Col: 0, Row: 0, Button: MouseWheelUp, Action: MousePress},
			"\x1b[M\x60\x21\x21",
		},
		{
			// A release names no button in this encoding; it is button 3.
			"release",
			MouseEvent{Col: 0, Row: 0, Button: MouseNone, Action: MouseRelease},
			"\x1b[M\x23\x21\x21",
		},
		{
			// The last coordinate the packing can hold: 222 lands on byte 255.
			"largest representable coordinate",
			MouseEvent{Col: 222, Row: 222, Button: MouseLeft, Action: MousePress},
			"\x1b[M\x20\xff\xff",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := tc.ev
			ev.Enc = MouseX10
			got, ok := ev.Encode()
			if !ok {
				t.Fatalf("Encode reported the event unrepresentable")
			}
			if got != tc.want {
				t.Errorf("Encode() = % x, want % x", got, tc.want)
			}
		})
	}

	// Past 222 the field has no representation at all, and approximating it
	// would hand the program a coordinate the user never clicked.
	over := MouseEvent{Col: 223, Row: 0, Button: MouseLeft, Action: MousePress, Enc: MouseX10}
	if got, ok := over.Encode(); ok {
		t.Errorf("column 223 encoded as % x, want a refusal", got)
	}

	// A release naming a button and a press naming none collide on button 3,
	// so neither can be encoded without the reader getting the other one back.
	for _, ev := range []MouseEvent{
		{Col: 1, Row: 1, Button: MouseLeft, Action: MouseRelease, Enc: MouseX10},
		{Col: 1, Row: 1, Button: MouseNone, Action: MousePress, Enc: MouseX10},
		{Col: 1, Row: 1, Button: MouseLeft, Action: MouseRelease, Enc: MouseURXVT},
	} {
		if got, ok := ev.Encode(); ok {
			t.Errorf("%+v encoded as %q, want a refusal", ev, got)
		}
	}

	// urxvt is the same packing written as decimal parameters, which lifts the
	// coordinate limit but keeps everything else.
	urxvt := MouseEvent{Col: 300, Row: 9, Button: MouseLeft, Action: MousePress, Enc: MouseURXVT}
	if got, ok := urxvt.Encode(); !ok || got != "\x1b[32;301;10M" {
		t.Errorf("urxvt = %q, %v, want \\x1b[32;301;10M", got, ok)
	}
}
