package tape

import (
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
