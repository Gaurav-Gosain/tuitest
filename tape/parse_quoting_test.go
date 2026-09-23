package tape

import (
	"errors"
	"strings"
	"testing"
)

// An argument with a space in it had no spelling: Spawn, Set and the key
// attributes split on whitespace, so "Spawn sh -c \"echo hi\"" ran sh with the
// arguments `"echo` and `hi"`, and a recording of such a command printed a tape
// that ran something else. A token that starts with a double quote is now read
// as a Go-quoted string.
//
// Verified to fail without the fix: every case below either splits the quoted
// argument or rejects it.
func TestParseQuotedArguments(t *testing.T) {
	cases := []struct {
		src   string
		check func(Command) bool
	}{
		{`Spawn sh -c "echo hi; exit 3"`, func(c Command) bool {
			return equalStrings(c.Argv, []string{"sh", "-c", "echo hi; exit 3"})
		}},
		{`Spawn "my prog" ""`, func(c Command) bool {
			return equalStrings(c.Argv, []string{"my prog", ""})
		}},
		{`Set Env "GREETING=hello world"`, func(c Command) bool {
			return equalStrings(c.SetArgs, []string{"GREETING=hello world"})
		}},
		{`Key Space +Text " "`, func(c Command) bool {
			return c.KeyAttrs.Text == " " && equalStrings(c.Keys, []string{"Space"})
		}},
		{`Key a +Shifted " " +Base "\""`, func(c Command) bool {
			return c.KeyAttrs.Shifted == " " && c.KeyAttrs.Base == `"`
		}},
		// The quote only opens a string where an argument is free-form. A Key
		// line still names the double quote key with a bare quote.
		{`Key "`, func(c Command) bool { return equalStrings(c.Keys, []string{`"`}) }},
		{`Key " "`, func(c Command) bool { return equalStrings(c.Keys, []string{`"`, `"`}) }},
		// A bare word is unchanged, including one with a quote inside it.
		{`Spawn printf a"b`, func(c Command) bool { return equalStrings(c.Argv, []string{"printf", `a"b`}) }},
	}
	for _, tc := range cases {
		cmds, err := Parse(strings.NewReader(tc.src))
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.src, err)
			continue
		}
		if !tc.check(cmds[0]) {
			t.Errorf("Parse(%q) = %+v", tc.src, cmds[0])
		}
	}
}

// A malformed quoted argument is a positioned parse error, not a silent split.
func TestParseQuotedArgumentErrors(t *testing.T) {
	cases := []struct {
		src     string
		wantCol int
		wantMsg string
	}{
		{`Spawn sh -c "echo`, 13, "unterminated quoted argument"},
		{`Spawn sh "a"b`, 13, "followed by a space"},
		{`Set Env "A=1`, 9, "unterminated quoted argument"},
		{`Key a +Text x`, 7, "+Text needs a quoted string"},
	}
	for _, tc := range cases {
		_, err := Parse(strings.NewReader(tc.src))
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("Parse(%q) error = %v, want a *ParseError", tc.src, err)
			continue
		}
		if pe.Col != tc.wantCol || !strings.Contains(pe.Msg, tc.wantMsg) {
			t.Errorf("Parse(%q) = col %d %q, want col %d containing %q", tc.src, pe.Col, pe.Msg, tc.wantCol, tc.wantMsg)
		}
	}
}

// What the printer writes for an argument with a space, an empty argument, or
// a control character must read back unchanged. This is the path a recording
// takes: the recorder builds commands from argv and the environment, and Print
// writes them.
//
// Verified to fail without the fix: the Spawn and Env cases print bare and
// re-parse to different arguments.
func TestPrintQuotesArgumentsThatNeedIt(t *testing.T) {
	cmds := []Command{
		{Kind: KindSet, SetKey: "Env", SetArgs: []string{"A=b c"}},
		{Kind: KindSet, SetKey: "Env", SetArgs: []string{"TAB=\t"}},
		{Kind: KindSpawn, Argv: []string{"sh", "-c", "echo hi", "", `"quoted"`, "bad\xffutf8", "nl\n"}},
		{Kind: KindKey, Keys: []string{"a"}, KeyAttrs: KeyAttrs{Shifted: " ", Base: "\n"}},
	}
	src := Sprint(cmds)
	back, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("printed tape does not parse: %v\n%s", err, src)
	}
	if len(back) != len(cmds) {
		t.Fatalf("got %d commands back, want %d\n%s", len(back), len(cmds), src)
	}
	for i := range cmds {
		if d := commandDiff(cmds[i], back[i]); d != "" {
			t.Errorf("command %d changed: %s\n%s", i, d, src)
		}
	}
	// An ordinary tape keeps its bare spelling.
	if got := (Command{Kind: KindSpawn, Argv: []string{"less", "README.md"}}).String(); got != "Spawn less README.md" {
		t.Errorf("plain Spawn printed as %q", got)
	}
}

// A kitty keyboard report for the space bar with associated text carries the
// text " ". The tape had no spelling for it, since `+Text " "` split into two
// broken tokens, so the recorder's representability check fell back to writing
// the report as an opaque Raw line. It now records as a readable Key.
//
// Verified to fail without the fix: the recording is a Raw command.
func TestRecordedKittySpaceWithTextParses(t *testing.T) {
	rec := NewRecorder()
	rec.SetModes(Modes{KittyFlags: 0b11111})
	rec.Input([]byte("\x1b[32;1;32u"))
	cmds := rec.Commands()
	src := Sprint(cmds)
	back, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("recorded tape does not parse: %v\n%s", err, src)
	}
	if len(back) != 1 || back[0].KeyAttrs.Text != " " {
		t.Fatalf("recorded key lost its text: %s", src)
	}
}

// A UTF-8 byte order mark, which some editors write at the start of a file,
// used to glue itself to the first verb and fail as `unknown command
// "\ufeffSpawn", did you mean "Spawn"?`.
//
// Verified to fail without the fix.
func TestParseSkipsByteOrderMark(t *testing.T) {
	cmds, err := Parse(strings.NewReader("\ufeffSpawn x\nKey Enter\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 || cmds[0].Kind != KindSpawn {
		t.Fatalf("got %+v", cmds)
	}
}

// A line past the scanner's limit used to return the scanner's own error,
// "bufio.Scanner: token too long", which names neither the file nor the line.
//
// Verified to fail without the fix: the error is not a *ParseError.
func TestParseReportsOverlongLineWithPosition(t *testing.T) {
	src := "Spawn x\nKey Enter\nRaw \"" + strings.Repeat("a", maxLineBytes) + "\"\n"
	_, err := ParseNamed(strings.NewReader(src), "big.tape")
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want a *ParseError", err)
	}
	if pe.File != "big.tape" || pe.Line != 3 {
		t.Errorf("position = %s:%d, want big.tape:3", pe.File, pe.Line)
	}
}
