package tape

import (
	"errors"
	"strings"
	"testing"
)

// An argument a verb would ignore is a parse error. Each of these used to parse
// and then do something other than what it reads as: WaitStable /ready/ passed
// without "ready" ever appearing, Expect /x/ @5s asserted at once instead of
// waiting, a bare Wait passed validation and failed only when played, Wait //
// matched every screen, and Hide junk dropped the junk.
//
// Verified to fail without the fix: every case parses.
func TestParseRejectsArgumentsAVerbIgnores(t *testing.T) {
	cases := []struct {
		src     string
		wantCol int
		wantMsg string
	}{
		{"WaitStable /ready/", 12, "WaitStable does not take a /regex/"},
		{"Wait Stable /ready/", 13, "WaitStable does not take a /regex/"},
		{"WaitOutput +Line", 12, "WaitOutput does not take +Line"},
		{"WaitPrompt +Screen @1s", 12, "WaitPrompt does not take +Screen"},
		{"WaitCommand /done/ @1s", 13, "WaitCommand does not take a /regex/"},
		{"WaitStable soon", 12, `unexpected token "soon" (want @timeout)`},
		{"Expect /x/ @5s", 12, "Expect does not wait, so it takes no @timeout"},
		{"Expect /x/ junk", 12, `unexpected token "junk" (want /regex/, +Screen, or +Line)`},
		{"Wait", 1, "Wait needs a /regex/"},
		{"Wait @5s", 1, "Wait needs a /regex/"},
		{"Wait //", 6, "empty /regex/"},
		{"Expect // +Line", 8, "empty /regex/"},
		{"Hide now", 6, "Hide takes no arguments"},
		{"Show x", 6, "Show takes no arguments"},
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

	// The arguments each verb does take still parse.
	for _, src := range []string{
		"Wait /x/ +Line @2s", "WaitStable @1s", "Wait Stable @1s", "WaitOutput @1s",
		"WaitPrompt", "WaitCommand @3s", "Expect /x/ +Screen", "Hide", "Show",
	} {
		if _, err := Parse(strings.NewReader(src)); err != nil {
			t.Errorf("Parse(%q): %v", src, err)
		}
	}
}

// A Snapshot name is joined onto the golden directory, and with -update the
// file is written. A tape is untrusted input, so a name that climbs out of the
// directory must be refused before anything touches the disk.
//
// Verified to fail without the fix: the names parse. The player's own check is
// TestPlayerRefusesSnapshotOutsideGoldenDir.
func TestSnapshotNameStaysInsideGoldenDir(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/etc/passwd", ".."} {
		_, err := Parse(strings.NewReader("Snapshot " + name))
		var pe *ParseError
		if !errors.As(err, &pe) || pe.Col != 10 || !strings.Contains(pe.Msg, "relative path inside the golden directory") {
			t.Errorf("Snapshot %s: error = %v, want a parse error at column 10", name, err)
		}
	}
	if _, err := Parse(strings.NewReader("Snapshot group/step-01")); err != nil {
		t.Errorf("a name with a subdirectory should parse: %v", err)
	}
}
