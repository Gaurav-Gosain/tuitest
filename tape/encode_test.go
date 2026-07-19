package tape_test

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// TestEncodeParseRoundTrip is the property the whole record/replay loop rests
// on: anything Encode writes, Parse must read back to the same commands. A
// recorded tape that did not survive a write-and-read would replay as something
// other than what was recorded.
func TestEncodeParseRoundTrip(t *testing.T) {
	source := strings.Join([]string{
		"Set Size 100 30",
		"Set Term xterm",
		"Set Env FOO=bar",
		"Set WaitTimeout 3s",
		"Spawn /bin/prog -x",
		"Type hello world",
		"Key Enter Tab Ctrl+c",
		"Key Alt+x F5 Space",
		`Wait /echo:\s+hello/ +Screen`,
		"Wait /ready/ +Line @2s",
		"WaitStable",
		"WaitStable @4s",
		"WaitPrompt",
		"WaitCommand",
		`Expect /done/ +Screen`,
		"ExpectExit 3",
		"Snapshot step-01",
		"Snapshot step-02 +Styled",
		"Resize 120 40",
		"Hide",
		"Show",
		"Sleep 1.5s",
	}, "\n")

	cmds, err := tape.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatalf("parsing the source: %v", err)
	}

	encoded := strings.TrimRight(tape.Encode(cmds), "\n")
	if encoded != source {
		t.Errorf("encode did not reproduce the source:\n got:\n%s\nwant:\n%s", encoded, source)
	}

	// Re-parsing the encoded form must yield the same commands, compared on the
	// fields that drive behavior.
	again, err := tape.Parse(strings.NewReader(encoded))
	if err != nil {
		t.Fatalf("re-parsing the encoded tape: %v", err)
	}
	if len(again) != len(cmds) {
		t.Fatalf("re-parse produced %d commands, want %d", len(again), len(cmds))
	}
	for i := range cmds {
		a, b := cmds[i], again[i]
		if a.Kind != b.Kind || a.Text != b.Text || a.Name != b.Name ||
			a.Code != b.Code || a.Dur != b.Dur || a.Scope != b.Scope ||
			a.Cols != b.Cols || a.Rows != b.Rows || a.Styled != b.Styled ||
			a.HasRegex != b.HasRegex || a.HasTimeout != b.HasTimeout ||
			a.Timeout != b.Timeout ||
			strings.Join(a.Keys, ",") != strings.Join(b.Keys, ",") ||
			strings.Join(a.Argv, ",") != strings.Join(b.Argv, ",") ||
			strings.Join(a.SetArgs, ",") != strings.Join(b.SetArgs, ",") ||
			a.SetKey != b.SetKey {
			t.Errorf("command %d differs after a round trip:\n%#v\n%#v", i, a, b)
		}
		if a.HasRegex && a.Regex.String() != b.Regex.String() {
			t.Errorf("command %d regex differs: %q vs %q", i, a.Regex, b.Regex)
		}
	}
}

// TestParseResize covers the verb added for recording terminal resizes.
func TestParseResize(t *testing.T) {
	cmds, err := tape.Parse(strings.NewReader("Resize 120 40"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Kind != tape.KindResize {
		t.Fatalf("expected one Resize command, got %#v", cmds)
	}
	if cmds[0].Cols != 120 || cmds[0].Rows != 40 {
		t.Errorf("Resize parsed as %dx%d, want 120x40", cmds[0].Cols, cmds[0].Rows)
	}

	for _, bad := range []string{"Resize", "Resize 80", "Resize 80 x", "Resize 0 40", "Resize 80 -1"} {
		if _, err := tape.Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("%q parsed but should have been rejected", bad)
		}
	}
}
