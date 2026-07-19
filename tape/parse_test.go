package tape

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

func TestParseBasic(t *testing.T) {
	src := `
# a comment
Set Size 80 24
Set Term xterm-256color
Spawn echotui
Type echo hello world
Key Ctrl+b %
Wait /hello/ +Line @3s
WaitStable @2s
Expect /world/ +Screen
ExpectExit 0
Snapshot greeting
Snapshot fancy +Styled
Hide
Show
Sleep 100ms
`
	cmds, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []Kind{
		KindSet, KindSet, KindSpawn, KindType, KindKey, KindWait, KindWaitStable,
		KindExpect, KindExpectExit, KindSnapshot, KindSnapshot, KindHide, KindShow, KindSleep,
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %d commands, want %d", len(cmds), len(want))
	}
	for i, k := range want {
		if cmds[i].Kind != k {
			t.Errorf("cmd %d kind = %d, want %d", i, cmds[i].Kind, k)
		}
	}

	typeCmd := cmds[3]
	if typeCmd.Text != "echo hello world" {
		t.Errorf("Type text = %q", typeCmd.Text)
	}
	waitCmd := cmds[5]
	if !waitCmd.HasRegex || waitCmd.Regex.String() != "hello" {
		t.Errorf("Wait regex = %v", waitCmd.Regex)
	}
	if waitCmd.Scope != tuitest.ScopeLastLine {
		t.Errorf("Wait scope = %d, want ScopeLastLine", waitCmd.Scope)
	}
	if !waitCmd.HasTimeout || waitCmd.Timeout != 3*time.Second {
		t.Errorf("Wait timeout = %v", waitCmd.Timeout)
	}
	if !cmds[10].Styled {
		t.Error("Snapshot fancy should be styled")
	}
	if cmds[13].Dur != 100*time.Millisecond {
		t.Errorf("Sleep dur = %v", cmds[13].Dur)
	}
}

func TestParseWaitStableWord(t *testing.T) {
	cmds, err := Parse(strings.NewReader("Wait Stable"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].Kind != KindWaitStable {
		t.Fatalf("Wait Stable did not parse to KindWaitStable: %+v", cmds)
	}
}

// TestParseBareVerb covers the lines that carry a verb and nothing else. The
// Type case used to slice past the end of the line and panic.
func TestParseBareVerb(t *testing.T) {
	cmds, err := Parse(strings.NewReader("Type\nHide\nShow\nWait\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 4 {
		t.Fatalf("got %d commands, want 4", len(cmds))
	}
	if cmds[0].Kind != KindType || cmds[0].Text != "" {
		t.Errorf("bare Type = %+v, want KindType with empty text", cmds[0])
	}
}

// TestParseRegexWithSpacesAndSlashes pins the rule that a pattern runs from the
// first slash on the line to the last one, so it may contain both spaces and
// slashes. Splitting on whitespace first used to mangle either case.
func TestParseRegexWithSpacesAndSlashes(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"Wait /a b/", "a b"},
		{"Wait /a  b/", "a  b"},
		{"Wait /usr/local/bin/ +Line @3s", "usr/local/bin"},
		{"Wait //", ""},
		{"Expect /x/ +Screen", "x"},
	}
	for _, tc := range cases {
		cmds, err := Parse(strings.NewReader(tc.src))
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.src, err)
			continue
		}
		if !cmds[0].HasRegex || cmds[0].Regex.String() != tc.want {
			t.Errorf("Parse(%q) regex = %q, want %q", tc.src, cmds[0].Regex, tc.want)
		}
	}
	if _, err := Parse(strings.NewReader("Wait /unterminated")); err == nil {
		t.Error("an unterminated /regex/ should be an error")
	}
	last := func(src string) Command {
		cmds, err := Parse(strings.NewReader(src))
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		return cmds[0]
	}
	if c := last("Wait /usr/local/bin/ +Line @3s"); c.Scope != tuitest.ScopeLastLine || c.Timeout != 3*time.Second {
		t.Errorf("options outside the regex were dropped: %+v", c)
	}
}

// TestParseWaitStableTakesOptions covers "Wait Stable @2s", which used to be
// rejected because only a bare "Wait Stable" was recognised.
func TestParseWaitStableTakesOptions(t *testing.T) {
	cmds, err := Parse(strings.NewReader("Wait Stable @2s"))
	if err != nil {
		t.Fatal(err)
	}
	if cmds[0].Kind != KindWaitStable || cmds[0].Timeout != 2*time.Second {
		t.Errorf("Wait Stable @2s = %+v", cmds[0])
	}
}

// TestParseRoundTrip checks that printing a parsed tape and parsing it again
// yields the same commands, the property FuzzParse generalizes.
func TestParseRoundTrip(t *testing.T) {
	src := `Set Size 40 10
Spawn ./app --flag
Type   spaced   text
Key Ctrl+b %
Wait /a  b/c/ +Line @3s
WaitStable @2s
Expect /done/
Snapshot final +Styled
Sleep 10ms
ExpectExit 0
`
	cmds, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Print(&buf, cmds); err != nil {
		t.Fatal(err)
	}
	if buf.String() != src {
		t.Errorf("Print did not reproduce the source tape:\ngot:\n%s\nwant:\n%s", buf.String(), src)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"Nonsense",
		"Set",
		"Set Size 80",
		"Set Bogus x",
		"Spawn",
		"ExpectExit notanumber",
		"Expect +Screen",       // no regex
		"Sleep",                // no duration
		"Key",                  // no key
		"Key Ctrl+notarealkey", // multi-char base with Ctrl
		"Sleep 0s",             // non-positive durations are always a mistake
		"Sleep -5s",
		"Wait /x/ @0s",
		"Wait /x/ @-1s",
		"Set WaitTimeout -1s",
		"Set StabilizeInterval 0s",
		"Set Size 0 10",       // a zero dimension cannot be spawned
		"Set Size 100000 100", // an absurd grid would be allocated up front
		"Set Size 100 -4",
		"Snapshot name +Styleed", // a mistyped flag must not be ignored
		"Wait /unterminated",
	}
	for _, src := range cases {
		if _, err := Parse(strings.NewReader(src)); err == nil {
			t.Errorf("expected parse error for %q", src)
		}
	}
}

func TestResolveKey(t *testing.T) {
	cases := []struct {
		tok  string
		want tuitest.Key
	}{
		{"Enter", tuitest.Enter},
		{"Ctrl+b", tuitest.Ctrl('b')},
		{"Alt+x", tuitest.Alt("x")},
		{"%", tuitest.Key("%")},
		{"Up", tuitest.Up},
	}
	for _, tc := range cases {
		got, err := ResolveKey(tc.tok)
		if err != nil {
			t.Errorf("ResolveKey(%q): %v", tc.tok, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveKey(%q) = %q, want %q", tc.tok, got, tc.want)
		}
	}
}

// TestParseAcceptsCRLF covers a tape authored on Windows, or edited by a tool
// that rewrote the line endings.
//
// This one passes without the TrimRight in ParseNamed, because bufio.Scanner
// already strips a single trailing CR. It is kept as a regression guard on the
// behaviour rather than on the implementation; the case that actually needs
// the TrimRight is TestParseStripsResidualCarriageReturns.
func TestParseAcceptsCRLF(t *testing.T) {
	cmds, err := Parse(strings.NewReader("Spawn prog\r\nType hello\r\nKey Enter\r\n"))
	if err != nil {
		t.Fatalf("a CRLF tape did not parse: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("parsed %d commands, want 3", len(cmds))
	}
	if cmds[1].Text != "hello" {
		t.Errorf("Type text = %q, want %q (a stray CR survived)", cmds[1].Text, "hello")
	}
	if got := cmds[2].Keys[0]; got != "Enter" {
		t.Errorf("Key = %q, want %q (a stray CR survived)", got, "Enter")
	}
}

// TestParseCRLFSurvivesReprint pins that a CRLF tape and its Unix equivalent
// print identically, so reformatting one does not silently change it.
func TestParseCRLFSurvivesReprint(t *testing.T) {
	crlf, err := Parse(strings.NewReader("Spawn prog\r\nType hi\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	lf, err := Parse(strings.NewReader("Spawn prog\nType hi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := Sprint(crlf), Sprint(lf); a != b {
		t.Errorf("CRLF and LF tapes print differently:\n%q\nvs\n%q", a, b)
	}
}

// TestParseStripsResidualCarriageReturns is the case ParseNamed's TrimRight
// exists for. bufio.Scanner removes exactly one trailing CR, so a file that
// went through more than one CRLF conversion, or that carries a lone CR before
// the newline, still hands the parser a line ending in CR. That CR lands
// inside the final token: a Key name stops resolving, and a Type line sends an
// invisible carriage return to the program under test.
//
// Verified to fail: replacing the TrimRight with a bare sc.Text() makes the
// Key case fail to resolve "Enter" and leaves a CR on the Type text.
func TestParseStripsResidualCarriageReturns(t *testing.T) {
	cmds, err := Parse(strings.NewReader("Spawn prog\r\r\nType hello\r\r\nKey Enter\r\r\n"))
	if err != nil {
		t.Fatalf("a doubled-CR tape did not parse: %v", err)
	}
	if len(cmds) != 3 {
		t.Fatalf("parsed %d commands, want 3", len(cmds))
	}
	if cmds[1].Text != "hello" {
		t.Errorf("Type text = %q, want %q (a residual CR survived)", cmds[1].Text, "hello")
	}
	if got := cmds[2].Keys[0]; got != "Enter" {
		t.Errorf("Key = %q, want %q (a residual CR survived)", got, "Enter")
	}
}
