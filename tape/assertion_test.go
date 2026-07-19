package tape_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// play parses and runs a tape against the fixture, returning the error.
func play(t *testing.T, script string) error {
	t.Helper()
	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tape.NewPlayer().Run(cmds)
}

// A failed Expect must be an AssertionError, since that is what the CLI keys
// its exit code off. Verified to fail: returning fmt.Errorf from Player.expect
// makes errors.As find nothing and fails this test.
func TestFailedExpectIsAnAssertionError(t *testing.T) {
	err := play(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /ECHOTUI/ @5s\nExpect /nowhere/\n")
	if err == nil {
		t.Fatal("want an assertion failure")
	}
	// A failed Expect reports as *tape.ExpectError, which carries the screen so
	// replay can render it. It must still satisfy tape.AssertionFailure, since
	// that is what the CLI classifies the exit code off.
	var af tape.AssertionFailure
	if !errors.As(err, &af) {
		t.Fatalf("error is %T, want a tape.AssertionFailure: %v", err, err)
	}
	var ee *tape.ExpectError
	if !errors.As(err, &ee) {
		t.Fatalf("error is %T, want *tape.ExpectError: %v", err, err)
	}
	if !strings.Contains(ee.Want, "nowhere") {
		t.Errorf("Want %q does not name the pattern", ee.Want)
	}
	if !strings.Contains(ee.Detail, "ECHOTUI") {
		t.Errorf("Detail does not include the screen:\n%s", ee.Detail)
	}
	if !strings.Contains(ee.Screen, "ECHOTUI") {
		t.Errorf("Screen was not captured:\n%s", ee.Screen)
	}
}

// The failure has to be attributable to a tape line, or a long tape gives no
// clue where to look. Verified to fail: dropping the LineError wrapper in
// Player.Run.
func TestAssertionFailureCarriesTheTapeLine(t *testing.T) {
	err := play(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /ECHOTUI/ @5s\nExpect /nowhere/\n")
	var le *tape.LineError
	if !errors.As(err, &le) {
		t.Fatalf("error is %T, want a *tape.LineError: %v", err, err)
	}
	if le.Line != 4 {
		t.Errorf("Line = %d, want 4 (the Expect line)", le.Line)
	}
}

// For a literal pattern the message must line up expected against actual and
// mark where they diverge, which is the difference between a usable failure and
// a wall of text.
// Verified to fail: removing the closest-line block from explainNoMatch, and
// an off-by-one in firstDiffColumn, each break this test.
func TestLiteralExpectFailureMarksTheFirstDifference(t *testing.T) {
	err := play(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType hello\nKey Enter\nWait /echo: hello/ @5s\nExpect /echo: hellp/\n")
	if err == nil {
		t.Fatal("want an assertion failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "echo: hellp") {
		t.Errorf("message does not show what was wanted:\n%s", msg)
	}
	if !strings.Contains(msg, "echo: hello") {
		t.Errorf("message does not show the closest actual line:\n%s", msg)
	}
	// "echo: hell" is common; the first difference is the eleventh rune.
	if !strings.Contains(msg, "first difference at column 11") {
		t.Errorf("message does not mark the first difference:\n%s", msg)
	}
}

// A regex with metacharacters has no single "expected string", so the message
// must not invent a misleading want/got comparison.
// Verified to fail: dropping the literal check in explainNoMatch makes this
// print a bogus want line built from the raw pattern.
func TestNonLiteralExpectFailureShowsOnlyTheScreen(t *testing.T) {
	err := play(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /ECHOTUI/ @5s\nExpect /^zzz.*qqq$/\n")
	if err == nil {
		t.Fatal("want an assertion failure")
	}
	msg := err.Error()
	if strings.Contains(msg, "first difference") {
		t.Errorf("invented a character-level difference for a regex:\n%s", msg)
	}
	if !strings.Contains(msg, "ECHOTUI") {
		t.Errorf("message does not show the screen:\n%s", msg)
	}
}

// Verified to fail: returning fmt.Errorf from expectExit, or dropping either
// status from the message.
func TestExitCodeMismatchIsAnAssertionError(t *testing.T) {
	err := play(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType boom\nKey Enter\nExpectExit 0\n")
	var ae *tape.AssertionError
	if !errors.As(err, &ae) {
		t.Fatalf("error is %T, want *tape.AssertionError: %v", err, err)
	}
	if ae.Op != "ExpectExit" {
		t.Errorf("Op = %q, want \"ExpectExit\"", ae.Op)
	}
	msg := err.Error()
	if !strings.Contains(msg, "exit status 0") || !strings.Contains(msg, "exit status 3") {
		t.Errorf("message does not contrast the two statuses:\n%s", msg)
	}
}

// An explicit override has to beat the tape's own Set line.
// Verified to fail: removing the OverrideCols guard from applySet lets the tape
// win, and the 60-character echo then wraps and never matches on one line.
func TestOverrideBeatsTapeSetSize(t *testing.T) {
	long := strings.Repeat("x", 60)
	script := "Set Size 40 10\nSpawn " + echoBin +
		"\nWait /ECHOTUI/ @5s\nType " + long + "\nKey Enter\nWait /echo: " + long + "/ @5s\n"
	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}

	p := tape.NewPlayer()
	p.OverrideCols, p.OverrideRows = 100, 10
	if err := p.Run(cmds); err != nil {
		t.Errorf("override did not take effect: %v", err)
	}

	// Confirm the tape really does fail at its own size, so the assertion above
	// is evidence of the override rather than of a tape that passes either way.
	cmds2, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}
	if err := tape.NewPlayer().Run(cmds2); err == nil {
		t.Error("tape passed at 40 columns, so the override proves nothing")
	}
}

// Verified to fail: making ExtraEnv unused in applyOverrides.
func TestExtraEnvReachesTheProgram(t *testing.T) {
	script := "Set Size 40 10\nSpawn " + envScript(t) + "\nWait /MARK=zebra/ @5s\n"
	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}
	p := tape.NewPlayer()
	p.ExtraEnv = []string{"TUITEST_MARK=zebra"}
	if err := p.Run(cmds); err != nil {
		t.Errorf("environment variable did not reach the program: %v", err)
	}
}

// envScript writes an executable script that prints an environment variable
// into the test's own temp dir, which testing removes afterwards.
func envScript(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	path := filepath.Join(t.TempDir(), "env.sh")
	body := "#!" + sh + "\nprintf 'MARK=%s' \"$TUITEST_MARK\"\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
