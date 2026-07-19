package tape

import (
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// The round trip is the property that matters for generated tapes: a fuzz
// reproduction is only useful if the parser accepts exactly what the formatter
// wrote, byte payloads and all.
func TestFormatParseRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cmd  Command
	}{
		{"spawn with args", Command{Kind: KindSpawn, Argv: []string{"prog", "-flag", "value"}}},
		{"key", Command{Kind: KindKey, Keys: []string{"Ctrl+c"}}},
		{"several keys", Command{Kind: KindKey, Keys: []string{"Up", "Down", "F5"}}},
		{"resize", Command{Kind: KindResize, Cols: 1, Rows: 1}},
		{"large resize", Command{Kind: KindResize, Cols: 1000, Rows: 500}},
		{"sleep", Command{Kind: KindSleep, Dur: 250 * time.Millisecond}},
		{"expect exit", Command{Kind: KindExpectExit, Code: 3}},
		{"snapshot", Command{Kind: KindSnapshot, Name: "main"}},
		{"styled snapshot", Command{Kind: KindSnapshot, Name: "main", Styled: true}},
		{"hide", Command{Kind: KindHide}},
		{"wait stable", Command{Kind: KindWaitStable}},
		{"wait output with timeout", Command{Kind: KindWaitOutput, Timeout: 200 * time.Millisecond, HasTimeout: true}},

		// Payload-carrying commands. These are the ones a fuzz repro depends
		// on, and the ones a naive line-based grammar would mangle.
		{"raw escape", Command{Kind: KindRaw, Text: "\x1b[1;2;3m"}},
		{"raw truncated escape", Command{Kind: KindRaw, Text: "\x1b["}},
		{"raw invalid utf8", Command{Kind: KindRaw, Text: "\x80\xbf\xfe\xff"}},
		{"raw overlong encoding", Command{Kind: KindRaw, Text: "\xc0\xaf"}},
		{"raw with spaces", Command{Kind: KindRaw, Text: "  leading and trailing  "}},
		{"raw with newline", Command{Kind: KindRaw, Text: "line\nbreak"}},
		{"raw with quotes", Command{Kind: KindRaw, Text: `he said "hi"`}},
		{"raw empty", Command{Kind: KindRaw, Text: ""}},
		{"raw nul byte", Command{Kind: KindRaw, Text: "\x00\x01\x02"}},
		{"paste multiline", Command{Kind: KindPaste, Text: "one\ntwo\nthree"}},
		{"paste emoji", Command{Kind: KindPaste, Text: "\U0001f600\U0001f1ef\U0001f1f5"}},

		// Every mouse shape, since the modifier encoding is order sensitive.
		{"mouse press", Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{Col: 10, Row: 5}}},
		{"mouse release", Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{Col: 0, Row: 0, Action: tuitest.MouseRelease}}},
		{"mouse move", Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{Col: 3, Row: 4, Action: tuitest.MouseMove}}},
		{"wheel up", Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{Col: 1, Row: 1, Button: tuitest.MouseWheelUp}}},
		{"wheel down", Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{Col: 1, Row: 1, Button: tuitest.MouseWheelDown}}},
		{"middle button", Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{Button: tuitest.MouseMiddle}}},
		{
			"all modifiers",
			Command{Kind: KindMouse, Mouse: tuitest.MouseEvent{
				Col: 7, Row: 8,
				Button: tuitest.MouseRight,
				Mods:   tuitest.ModCtrl | tuitest.ModAlt | tuitest.ModShift,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text := Sprint([]Command{tc.cmd})
			got, err := Parse(strings.NewReader(text))
			if err != nil {
				t.Fatalf("Parse(Sprint(%+v)) failed: %v\nformatted as: %q", tc.cmd, err, text)
			}
			if len(got) != 1 {
				t.Fatalf("round trip produced %d commands, want 1, from %q", len(got), text)
			}
			assertSameCommand(t, tc.cmd, got[0], text)
		})
	}
}

// assertSameCommand compares the fields that carry meaning. Line numbers are
// assigned by the parser and are expected to differ.
func assertSameCommand(t *testing.T, want, got Command, text string) {
	t.Helper()

	if got.Kind != want.Kind {
		t.Fatalf("Kind = %d, want %d (formatted as %q)", got.Kind, want.Kind, text)
	}
	if got.Text != want.Text {
		t.Fatalf("Text = %q, want %q (formatted as %q)", got.Text, want.Text, text)
	}
	if got.Cols != want.Cols || got.Rows != want.Rows {
		t.Fatalf("size = %dx%d, want %dx%d", got.Cols, got.Rows, want.Cols, want.Rows)
	}
	if got.Mouse != want.Mouse {
		t.Fatalf("Mouse = %+v, want %+v (formatted as %q)", got.Mouse, want.Mouse, text)
	}
	if got.Code != want.Code {
		t.Fatalf("Code = %d, want %d", got.Code, want.Code)
	}
	if got.Name != want.Name || got.Styled != want.Styled {
		t.Fatalf("snapshot = %q styled=%v, want %q styled=%v", got.Name, got.Styled, want.Name, want.Styled)
	}
	if got.Dur != want.Dur {
		t.Fatalf("Dur = %s, want %s", got.Dur, want.Dur)
	}
	if got.HasTimeout != want.HasTimeout || got.Timeout != want.Timeout {
		t.Fatalf("timeout = %s (set=%v), want %s (set=%v)", got.Timeout, got.HasTimeout, want.Timeout, want.HasTimeout)
	}
	if strings.Join(got.Keys, ",") != strings.Join(want.Keys, ",") {
		t.Fatalf("Keys = %v, want %v", got.Keys, want.Keys)
	}
	if strings.Join(got.Argv, ",") != strings.Join(want.Argv, ",") {
		t.Fatalf("Argv = %v, want %v", got.Argv, want.Argv)
	}
}

// A whole tape must survive the round trip too, not just single lines, since a
// reproduction is a sequence.
func TestFormatParseRoundTripWholeTape(t *testing.T) {
	t.Parallel()

	want := []Command{
		{Kind: KindSet, SetKey: "Size", SetArgs: []string{"80", "24"}},
		{Kind: KindSpawn, Argv: []string{"prog"}},
		{Kind: KindWaitOutput},
		{Kind: KindResize, Cols: 1, Rows: 1},
		{Kind: KindRaw, Text: "\x1b[\x80"},
		{Kind: KindMouse, Mouse: tuitest.MouseEvent{Col: 2, Row: 3, Mods: tuitest.ModCtrl}},
		{Kind: KindPaste, Text: "pasted\ntext"},
		{Kind: KindKey, Keys: []string{"F5"}},
	}

	text := Sprint(want)
	got, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Parse(Sprint(tape)) failed: %v\ntape:\n%s", err, text)
	}
	if len(got) != len(want) {
		t.Fatalf("round trip produced %d commands, want %d:\n%s", len(got), len(want), text)
	}
	for i := range want {
		assertSameCommand(t, want[i], got[i], text)
	}
}

// Verified to fail on broken code: making Quote return the string unwrapped
// (`func Quote(s string) string { return s }`) makes every payload case in the
// round-trip tests fail, and this one reports the specific reason.
func TestRawPayloadWithSpacesIsNotSplitIntoFields(t *testing.T) {
	t.Parallel()

	// A grammar that split this line on whitespace would lose the interior
	// spacing, which is exactly what a hostile payload relies on.
	want := "  two  spaces  and\ttabs  "
	text := Sprint([]Command{{Kind: KindRaw, Text: want}})

	got, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Parse failed: %v (formatted as %q)", err, text)
	}
	if got[0].Text != want {
		t.Fatalf("Text = %q, want %q (formatted as %q)", got[0].Text, want, text)
	}
}
