package tape_test

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// A parse error has to point at the token that actually failed, not merely name
// the line, or the CLI cannot show a useful caret.
//
// Verified to fail: returning a bare fmt.Errorf from any of these cases loses
// the column and fails the check; returning a fixed column of 1 fails every row
// whose token is not first.
func TestParseErrorReportsLineAndColumn(t *testing.T) {
	cases := []struct {
		name     string
		tape     string
		wantLine int
		wantCol  int
		wantMsg  string
	}{
		{"unknown command", "Spawn x\nFrobnicate y\n", 2, 1, `unknown command "Frobnicate"`},
		{"unknown Set key", "Set Sizes 40 10\n", 1, 5, `unknown Set key "Sizes"`},
		{"non-integer size", "Set Size forty 10\n", 1, 10, "not an integer"},
		{"non-positive size", "Set Size 0 10\n", 1, 10, "must be positive"},
		{"bad Set duration", "Set WaitTimeout soon\n", 1, 17, "not a duration"},
		{"bad env entry", "Set Env NOEQUALS\n", 1, 9, "KEY=VALUE"},
		{"unknown wait token", "Wait /ok/ +Screne\n", 1, 11, `unexpected token "+Screne"`},
		{"bad timeout", "Wait /ok/ @soon\n", 1, 11, "not a duration"},
		{"bad regex", "Wait /(unclosed/\n", 1, 6, "regex"},
		{"unterminated regex", "Wait /ok +Screen\n", 1, 6, "unterminated"},
		{"bad exit code", "ExpectExit later\n", 1, 12, "not an integer"},
		{"bad sleep", "Sleep soon\n", 1, 7, "not a duration"},
		{"unknown key name", "Key Enter Nope\n", 1, 11, "Nope"},
		{"snapshot junk", "Snapshot name +Style\n", 1, 15, `unexpected token "+Style"`},
		{"expect without regex", "Expect +Screen\n", 1, 1, "needs a /regex/"},
		// The caret has to land on the second line's token, so the line number
		// must come from the source and not from a counter reset per command.
		{"error on a later line", "# comment\n\nSpawn ok\nSleep nope\n", 4, 7, "not a duration"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tape.Parse(strings.NewReader(tc.tape))
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want a parse error", tc.tape)
			}
			pe, ok := err.(*tape.ParseError)
			if !ok {
				t.Fatalf("error is %T, want *tape.ParseError: %v", err, err)
			}
			if pe.Line != tc.wantLine {
				t.Errorf("line = %d, want %d", pe.Line, tc.wantLine)
			}
			if pe.Col != tc.wantCol {
				t.Errorf("column = %d, want %d (message %q, line %q)", pe.Col, tc.wantCol, pe.Msg, pe.Text)
			}
			if !strings.Contains(pe.Msg, tc.wantMsg) {
				t.Errorf("message %q does not contain %q", pe.Msg, tc.wantMsg)
			}
		})
	}
}

// The rendered error is what a user reads, so the caret has to land under the
// reported column when the message is printed.
// Verified to fail: an off-by-one in caretPad moves the caret off the token.
func TestParseErrorRendersCaretUnderTheToken(t *testing.T) {
	_, err := tape.Parse(strings.NewReader("Wait /ok/ +Screne\n"))
	if err == nil {
		t.Fatal("want a parse error")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) < 3 {
		t.Fatalf("rendered error has %d lines, want the message, source, and caret:\n%s", len(lines), err)
	}
	source, caret := lines[1], lines[2]
	idx := strings.Index(caret, "^")
	if idx < 0 {
		t.Fatalf("no caret in rendered error:\n%s", err)
	}
	if idx >= len(source) || !strings.HasPrefix(source[idx:], "+Screne") {
		t.Errorf("caret at offset %d does not point at the bad token:\n%s\n%s", idx, source, caret)
	}
}

// A tab-indented tape must still line the caret up, since a tab is one column
// but several screen cells.
// Verified to fail: emitting a space instead of a tab in caretPad misaligns the
// caret whenever the line is tab indented.
func TestParseErrorCaretSurvivesTabIndentation(t *testing.T) {
	_, err := tape.Parse(strings.NewReader("\t\tSleep soon\n"))
	if err == nil {
		t.Fatal("want a parse error")
	}
	lines := strings.Split(err.Error(), "\n")
	caret := lines[len(lines)-1]
	pad := caret[strings.Index(caret, "| ")+2 : strings.Index(caret, "^")]
	if strings.Count(pad, "\t") != 2 {
		t.Errorf("caret padding %q does not reproduce the two leading tabs", pad)
	}
}

// ParseNamed attaches the source name so the CLI can print file:line:col.
// Verified to fail: dropping the File assignment in ParseNamed.
func TestParseNamedAttachesTheFileName(t *testing.T) {
	_, err := tape.ParseNamed(strings.NewReader("Bogus\n"), "login.tape")
	if err == nil {
		t.Fatal("want a parse error")
	}
	if !strings.HasPrefix(err.Error(), "login.tape:1:1:") {
		t.Errorf("error does not start with file:line:col:\n%s", err)
	}
}

// A bare "Type" line used to slice past the end of the string and panic, since
// the old verbPrefix always added one for the separating space.
// Verified to fail: restoring the old expression, raw[:idx+len(verb)+1],
// panics with a slice-bounds error and fails this test.
func TestParseBareTypeLineDoesNotPanic(t *testing.T) {
	cmds, err := tape.Parse(strings.NewReader("Type\n"))
	if err != nil {
		t.Fatalf("Parse(\"Type\") = %v, want an empty Type", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("got %d commands, want 1", len(cmds))
	}
	if cmds[0].Kind != tape.KindType {
		t.Errorf("kind = %v, want KindType", cmds[0].Kind)
	}
	if cmds[0].Text != "" {
		t.Errorf("text = %q, want empty", cmds[0].Text)
	}
}

// Type keeps the literal spacing of the rest of its line, which is what makes
// it usable for typing indented input.
// Verified to fail: switching typeArg to strings.Join(fields, " ") collapses
// the runs of spaces and fails this test.
func TestTypePreservesLiteralSpacing(t *testing.T) {
	cmds, err := tape.Parse(strings.NewReader("Type   two  spaces \n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cmds[0].Text, "  two  spaces "; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}
