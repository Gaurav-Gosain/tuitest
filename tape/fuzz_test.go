package tape

import (
	"bytes"
	"strings"
	"testing"
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
