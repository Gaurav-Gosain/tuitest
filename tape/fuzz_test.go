package tape

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// seedTapes are the corpus entries added in code. The committed corpus under
// testdata/fuzz holds the shapes that once crashed or failed to round-trip; Go
// runs both as ordinary unit tests, so they stay regression guards without
// anyone starting a fuzzing session.
var seedTapes = []string{
	"",
	"# just a comment\n",
	"Set Size 80 24\nSpawn sh\nType echo hi\nKey Enter\nExpectExit 0\n",
	"Wait /ready/ +Screen @5s\n",
	"Wait /a b  c/ +Line @1500ms\n",
	"Wait Stable\n",
	"WaitStable @2s\nWaitPrompt\nWaitCommand @3s\n",
	"Snapshot name +Styled\nHide\nShow\nSleep 10ms\n",
	"Type\n",
	"Type   leading and  internal   spacing\n",
	"Key Ctrl+b Alt+x Shift+a % Enter\n",
	"Set Env KEY=VALUE\nSet Term dumb\nSet WaitTimeout 1s\nSet StabilizeInterval 50ms\n",
	"Expect /a\\/b/\n",
	"Wait //\n",
}

// FuzzParse asserts two things about the tape front end. First, that parsing
// arbitrary bytes never panics: a tape is untrusted input to the CLI. Second,
// that anything which parses survives a trip through Print and back unchanged,
// which is the property that keeps the printer honest and catches parser rules
// that silently drop or merge arguments.
func FuzzParse(f *testing.F) {
	for _, s := range seedTapes {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		cmds, err := Parse(strings.NewReader(src))
		if err != nil {
			return
		}

		var buf bytes.Buffer
		if err := Print(&buf, cmds); err != nil {
			t.Fatalf("Print: %v", err)
		}
		again, err := Parse(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("printed tape does not re-parse: %v\nprinted:\n%q\nsource:\n%q", err, buf.String(), src)
		}
		if len(again) != len(cmds) {
			t.Fatalf("round trip changed command count: %d -> %d\nprinted:\n%q\nsource:\n%q",
				len(cmds), len(again), buf.String(), src)
		}
		for i := range cmds {
			if diff := commandDiff(cmds[i], again[i]); diff != "" {
				t.Fatalf("round trip changed command %d: %s\nprinted:\n%q\nsource:\n%q",
					i, diff, buf.String(), src)
			}
		}
	})
}

// FuzzParseErrorPosition asserts that every parse error points somewhere real:
// a line that exists in the source, a column no further than one past the end
// of that line, and the line's own text. A caret under the wrong token is worse
// than no caret, since the reader trusts it.
func FuzzParseErrorPosition(f *testing.F) {
	for _, s := range seedTapes {
		f.Add(s)
	}
	for _, s := range []string{
		"Wait /x/ +Screne", "Spawn sh -c \"echo", "Spawn \"a\"b", "Key a +Text x",
		"Set Env \"A=1", "WaitStable /x/", "Expect /x/ @1s", "Hide x", "Snapshot ../x",
		"\tKey\tNope", "Mouse Press Left 1", "Type ok\nFrob", "Key é +Text \"\\x\"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		_, err := Parse(strings.NewReader(src))
		if err == nil {
			return
		}
		pe, ok := err.(*ParseError)
		if !ok {
			// Only a read error may come back untyped, and a strings.Reader
			// has none.
			t.Fatalf("error is %T, want *ParseError: %v", err, err)
		}
		lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
		if pe.Line < 1 || pe.Line > len(lines) {
			t.Fatalf("line %d is outside the %d-line source: %v", pe.Line, len(lines), pe)
		}
		if pe.Col < 0 || pe.Col > utf8.RuneCountInString(pe.Text)+1 {
			t.Fatalf("column %d is outside line %q: %v", pe.Col, pe.Text, pe)
		}
		if pe.Text != "" && !strings.Contains(lines[pe.Line-1], strings.TrimSpace(pe.Text)) {
			t.Fatalf("error quotes %q, but line %d is %q", pe.Text, pe.Line, lines[pe.Line-1])
		}
		_ = pe.Error()
	})
}

// FuzzResolveKey asserts the key-name parser never panics on arbitrary tokens
// and never reports success for a key that would send nothing, which would make
// a tape's Key command a silent no-op.
func FuzzResolveKey(f *testing.F) {
	for _, s := range []string{
		"Enter", "Ctrl+b", "Alt+Left", "Shift+a", "%", "C+M+x", "Ctrl+", "+", "++",
		"Ctrl+Alt+Delete", "F12", "", "Ctrl+é", "Meta+x", "S+S+s",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, token string) {
		k, err := ResolveKey(token)
		if err != nil {
			return
		}
		if k == "" {
			t.Fatalf("ResolveKey(%q) succeeded but resolved to an empty sequence", token)
		}
	})
}

// commandDiff reports the first field in which two commands differ, ignoring
// Line (the printed tape has no comments or blank lines, so line numbers move).
func commandDiff(a, b Command) string {
	if a.Kind != b.Kind {
		return "Kind " + a.Kind.String() + " != " + b.Kind.String()
	}
	if a.SetKey != b.SetKey {
		return "SetKey " + a.SetKey + " != " + b.SetKey
	}
	if !equalStrings(a.SetArgs, b.SetArgs) {
		return "SetArgs differ"
	}
	if !equalStrings(a.Argv, b.Argv) {
		return "Argv differ"
	}
	if a.Text != b.Text {
		return "Text " + quote(a.Text) + " != " + quote(b.Text)
	}
	if !equalStrings(a.Keys, b.Keys) {
		return "Keys differ"
	}
	if a.HasRegex != b.HasRegex {
		return "HasRegex differs"
	}
	if a.HasRegex && a.Regex.String() != b.Regex.String() {
		return "Regex " + quote(a.Regex.String()) + " != " + quote(b.Regex.String())
	}
	if a.Scope != b.Scope {
		return "Scope differs"
	}
	if a.HasTimeout != b.HasTimeout || a.Timeout != b.Timeout {
		return "Timeout differs"
	}
	if a.Code != b.Code {
		return "Code differs"
	}
	if a.Name != b.Name || a.Styled != b.Styled {
		return "Snapshot name or +Styled differs"
	}
	if a.Dur != b.Dur {
		return "Dur differs"
	}
	// These were missing, so a printer that dropped a key attribute, changed a
	// mouse event or flipped a focus direction still passed the round trip.
	if a.KeyAttrs != b.KeyAttrs {
		return "KeyAttrs differ"
	}
	if a.Cols != b.Cols || a.Rows != b.Rows {
		return "Resize size differs"
	}
	if a.Mouse != b.Mouse {
		return "Mouse event differs"
	}
	if a.FocusIn != b.FocusIn {
		return "Focus direction differs"
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func quote(s string) string { return "\"" + s + "\"" }
