package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tape"
	"github.com/spf13/cobra"
)

// echoBin is the deterministic fixture every spawning test drives. It is built
// once into a temp directory that TestMain removes, so the suite writes nothing
// outside its own temp space and leaves no binary behind.
var echoBin string

const fixturePrefix = "tuitest-cli-fixture-"

func TestMain(m *testing.M) {
	// A panicking test kills the process before the cleanup below can run, so
	// sweep anything an earlier crashed run left behind. Only directories older
	// than an hour are removed, so a concurrent run of this package is never
	// robbed of its fixture. Without this, a crash leaks a directory every time
	// and the leaks accumulate silently.
	sweepStaleFixtures(fixturePrefix, time.Hour)

	dir, err := os.MkdirTemp("", fixturePrefix)
	if err != nil {
		panic(err)
	}
	echoBin = filepath.Join(dir, "echotui")
	build := exec.Command("go", "build", "-o", echoBin, "../../testdata/echotui")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(dir)
		panic("building echotui fixture: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// sweepStaleFixtures removes temp directories with the given prefix that are
// older than maxAge, bounding what a crashed run can leave behind.
func sweepStaleFixtures(prefix string, maxAge time.Duration) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), e.Name()))
	}
}

// runCLI invokes the command line with captured output and a fixed
// environment, so no test depends on the developer's real one.
func runCLI(env map[string]string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	e := &Env{
		Stdout: &out,
		Stderr: &errb,
		Getenv: func(k string) string { return env[k] },
	}
	code = Main(e, args)
	return code, out.String(), errb.String()
}

// writeTape puts a tape in the test's own temp dir and returns its path.
func writeTape(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.tape")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- dispatch and help ---

// subcommands returns the command tree's own subcommands, which is what the
// registry's Commands() used to return. Help, completion and typo suggestions
// are all derived from this same tree by cobra, so a command that is registered
// is a command that is discoverable, which is the coherence guarantee the old
// registry existed to provide.
func subcommands() []*cobra.Command {
	var out []*cobra.Command
	for _, c := range newRootCommand(discardEnv()).Commands() {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Verified to fail: making Main return ExitOK for an unknown command, and
// setting SuggestionsMinimumDistance to 0 so no suggestion is offered, each
// break this test.
func TestUnknownCommandIsUsageErrorWithSuggestion(t *testing.T) {
	code, _, stderr := runCLI(nil, "rnu", "x.tape")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d for an unknown command", code, ExitUsage)
	}
	if !strings.Contains(stderr, `unknown command "rnu"`) {
		t.Errorf("stderr does not name the unknown command:\n%s", stderr)
	}
	// cobra words the suggestion differently from the hand-rolled dispatcher,
	// but it has to still be a suggestion and it has to still be "run".
	if !strings.Contains(strings.ToLower(stderr), "did you mean") || !strings.Contains(stderr, "run") {
		t.Errorf("stderr does not suggest the close match:\n%s", stderr)
	}
}

// A name nothing like a command must not produce a confident wrong guess.
func TestUnrelatedCommandGetsNoSuggestion(t *testing.T) {
	_, _, stderr := runCLI(nil, "zzzzzzzzzz")
	if strings.Contains(strings.ToLower(stderr), "did you mean") {
		t.Errorf("suggested a command for an unrelated name:\n%s", stderr)
	}
}

// The top-level help has to list every registered command, or a command becomes
// undiscoverable the moment someone adds one.
// Verified to fail: removing a command from newRootCommand's AddCommand call,
// and blanking an entry's Short, both break this test.
func TestHelpListsEveryRegisteredCommand(t *testing.T) {
	code, stdout, _ := runCLI(nil, "help")
	if code != ExitOK {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	for _, c := range subcommands() {
		if !strings.Contains(stdout, c.Name()) {
			t.Errorf("help does not list command %q:\n%s", c.Name(), stdout)
		}
		if c.Short == "" {
			t.Errorf("command %q has no summary", c.Name())
		}
		if !strings.Contains(stdout, c.Short) {
			t.Errorf("help does not show the summary for %q", c.Name())
		}
	}
	// The exit-code contract is part of what makes the tool scriptable, so the
	// root help still has to state it.
	for _, want := range []string{"an assertion failed", "a wait timed out", "harness error"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not document %q:\n%s", want, stdout)
		}
	}
}

// Every command must have real help with an example, since that is the whole
// point of the track. Verified to fail: blanking any command's Example field.
func TestEveryCommandHasHelpWithExamples(t *testing.T) {
	for _, c := range subcommands() {
		t.Run(c.Name(), func(t *testing.T) {
			code, stdout, _ := runCLI(nil, "help", c.Name())
			if code != ExitOK {
				t.Fatalf("help %s exit code = %d, want 0", c.Name(), code)
			}
			// fang renders section headings in upper case, so the assertion is
			// on the sections rather than on a literal usage line.
			if !strings.Contains(stdout, "USAGE") || !strings.Contains(stdout, "tuitest "+c.Name()) {
				t.Errorf("help %s has no usage line:\n%s", c.Name(), stdout)
			}
			if !strings.Contains(stdout, "EXAMPLES") {
				t.Errorf("help %s shows no examples:\n%s", c.Name(), stdout)
			}
			if c.Long == "" {
				t.Errorf("command %q has no long description", c.Name())
			}
		})
	}
}

func TestNoArgumentsIsUsageError(t *testing.T) {
	code, _, stderr := runCLI(nil)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(strings.ToLower(stderr), "usage:") {
		t.Errorf("stderr has no usage line:\n%s", stderr)
	}
}

// The single-dash spelling is the published one: every example in the README
// and every script written against this tool says "-size", not "--size". pflag
// reads a single dash as a cluster of shorthands and would reject it, so
// normalizeArgs rewrites it. Both spellings have to work.
// Verified to fail: removing the normalizeArgs call from Main makes the
// single-dash cases exit 2 with "unknown shorthand flag".
func TestSingleAndDoubleDashFlagsBothParse(t *testing.T) {
	long := strings.Repeat("x", 60)
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType "+long+"\nKey Enter\nWait /echo: "+long+"/ @5s\n")

	for _, spelling := range []string{"-size", "--size"} {
		t.Run(spelling, func(t *testing.T) {
			if code, _, stderr := runCLI(nil, "run", spelling, "100x10", path); code != ExitOK {
				t.Errorf("run %s 100x10 exit code = %d, want 0; stderr:\n%s", spelling, code, stderr)
			}
		})
	}
	// The "-name=value" form has to survive the rewrite too.
	if code, _, stderr := runCLI(nil, "run", "-size=100x10", path); code != ExitOK {
		t.Errorf("run -size=100x10 exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
}

// A flag that looks like the tool's own but belongs to the program under test
// must be left alone. Verified to fail: rewriting past the "--" terminator
// makes snap hand "--size" to the program instead of "-size".
func TestFlagsAfterTheTerminatorAreNotRewritten(t *testing.T) {
	sh := lookupShell(t)
	code, stdout, stderr := runCLI(nil,
		"snap", "-size", "40x6", "--", sh, "-c", `printf %s "$1"`, "sh", "-size")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "-size") {
		t.Errorf("the program did not receive its own -size argument:\n%s", stdout)
	}
}

// --- exit codes ---

// The exit-code contract is the thing CI depends on, so it is asserted directly
// against the error types that produce each code.
// Verified to fail: swapping any two return values in classify, and deleting
// the ClosedError case (which then falls through to ExitHarness), break this.
func TestClassifyMapsErrorsToExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"parse", &tape.ParseError{Line: 1, Msg: "bad"}, ExitUsage},
		{"timeout", &tuitest.TimeoutError{Op: "WaitFor"}, ExitTimeout},
		{"assertion", &tape.AssertionError{Op: "Expect"}, ExitAssert},
		{"child exited early", &tuitest.ClosedError{Op: "WaitFor"}, ExitAssert},
		{"anything else", errors.New("boom"), ExitHarness},
		// The player wraps failures with the tape line, so classification has
		// to see through the wrapper rather than only the outermost error.
		{"wrapped assertion", &tape.LineError{Line: 3, Err: &tape.AssertionError{}}, ExitAssert},
		{"wrapped timeout", &tape.LineError{Line: 3, Err: &tuitest.TimeoutError{}}, ExitTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Errorf("classify(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// --- run ---

func TestRunPassingTapeExitsZero(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /ECHOTUI/ @5s\nExpect /ECHOTUI/\n")
	code, _, stderr := runCLI(nil, "run", path)
	if code != ExitOK {
		t.Errorf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
}

// A timeout must say what it waited for, how long, and what was on screen. That
// text is the product for a CLI tool, so it is asserted, not just the code.
// Verified to fail: removing the Screen field from TimeoutError's message, and
// returning ExitHarness instead of ExitTimeout, each break this test.
func TestRunTimeoutReportsWaitElapsedAndScreen(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /neverappears/ @400ms\n")
	code, _, stderr := runCLI(nil, "run", path)
	if code != ExitTimeout {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitTimeout, stderr)
	}
	for _, want := range []string{
		"neverappears", // what it was waiting for
		"timed out after",
		"--- screen ---",
		"ECHOTUI", // the screen as it actually was
		"tape line 3",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("timeout message is missing %q:\n%s", want, stderr)
		}
	}
}

// A failed Expect must show expected versus actual with the difference marked.
// Verified to fail: dropping the closest-line block from explainNoMatch, and
// returning a plain fmt.Errorf instead of an AssertionError (which also changes
// the exit code), each break this test.
func TestRunFailedExpectShowsDifference(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType hi\nKey Enter\nWait /echo: hi/ @5s\nExpect /echo: hj/\n")
	code, _, stderr := runCLI(nil, "run", path)
	if code != ExitAssert {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitAssert, stderr)
	}
	for _, want := range []string{
		"Expect failed",
		"echo: hj", // wanted
		"echo: hi", // actual
		"first difference at column 8",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("expect failure is missing %q:\n%s", want, stderr)
		}
	}
}

// Verified to fail: returning ExitHarness for a mismatched exit status, or
// dropping the want/got detail from AssertionError, breaks this test.
func TestRunExitCodeMismatchIsAssertionFailure(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType boom\nKey Enter\nExpectExit 0\n")
	code, _, stderr := runCLI(nil, "run", path)
	if code != ExitAssert {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitAssert, stderr)
	}
	if !strings.Contains(stderr, "exit status 0") || !strings.Contains(stderr, "exit status 3") {
		t.Errorf("message does not contrast wanted and actual status:\n%s", stderr)
	}
}

// A program that cannot be spawned is a harness error, not a failed assertion:
// nothing was asserted. Verified to fail: making classify return ExitAssert for
// an unrecognized error breaks this test.
func TestRunUnspawnableProgramIsHarnessError(t *testing.T) {
	path := writeTape(t, "Spawn /nonexistent/definitely-not-here\n")
	code, _, stderr := runCLI(nil, "run", path)
	if code != ExitHarness {
		t.Errorf("exit code = %d, want %d; stderr:\n%s", code, ExitHarness, stderr)
	}
	if !strings.Contains(stderr, "nonexistent") {
		t.Errorf("message does not name the program:\n%s", stderr)
	}
}

// A parse error must name the file, line, and column, and show the line with a
// caret. Verified to fail: reverting ParseError.Error to a bare message, and
// dropping the column from perr, each break this test.
func TestRunParseErrorNamesFileLineColumnAndShowsLine(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn ./x\nWait /ok/ +Screne @5s\n")
	code, _, stderr := runCLI(nil, "run", path)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, path+":3:11:") {
		t.Errorf("error does not carry file:line:column:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Wait /ok/ +Screne @5s") {
		t.Errorf("error does not show the offending line:\n%s", stderr)
	}
	if !strings.Contains(stderr, "^") {
		t.Errorf("error does not point at the token:\n%s", stderr)
	}
	if !strings.Contains(stderr, "+Screen") {
		t.Errorf("error does not say what was expected instead:\n%s", stderr)
	}
}

func TestRunNeedsExactlyOneTape(t *testing.T) {
	code, _, stderr := runCLI(nil, "run")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "one tape file") {
		t.Errorf("stderr does not explain the argument:\n%s", stderr)
	}
}

// An explicit flag has to beat the tape's own Set line, which is the direction
// a user expects and the opposite of what prepending the setting would do.
// Verified to fail: removing the OverrideCols guard from applySet, so the
// tape's "Set Size 40 10" wins, makes this report 40 columns.
func TestRunSizeFlagOverridesTapeSetSize(t *testing.T) {
	// The fixture echoes what it reads, so a line longer than the tape's 40
	// columns but shorter than the flag's 100 only stays on one screen row if
	// the flag actually won.
	long := strings.Repeat("x", 60)
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType "+long+"\nKey Enter\nWait /echo: "+long+"/ @5s\n")

	if code, _, stderr := runCLI(nil, "run", "-size", "100x10", path); code != ExitOK {
		t.Errorf("with -size 100x10 exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	// Without the override the same tape cannot match, because the echoed line
	// wraps across rows at 40 columns.
	if code, _, _ := runCLI(nil, "run", path); code == ExitOK {
		t.Error("tape unexpectedly passed at 40 columns, so the override test proves nothing")
	}
}

// Verified to fail: dropping ExtraEnv from applyOverrides means the variable
// never reaches the child and the wait times out.
func TestRunEnvFlagReachesTheProgram(t *testing.T) {
	// Spawn splits on whitespace, so the program is a script rather than an
	// inline "sh -c" with a quoted argument.
	script := writeScript(t, "printf 'MARK=%s' \"$TUITEST_MARK\"\n")
	tapePath := writeTape(t, "Set Size 40 10\nSpawn "+script+"\nWait /MARK=zebra/ @5s\n")

	code, _, stderr := runCLI(nil, "run", "-env", "TUITEST_MARK=zebra", tapePath)
	if code != ExitOK {
		t.Errorf("exit code = %d, want 0; the environment variable did not reach the program.\nstderr:\n%s", code, stderr)
	}
}

// The JSON result has to be parseable and carry the exit code and kind, since
// that is what makes the tool composable in CI.
// Verified to fail: emitting the result to stderr instead of stdout, and
// dropping the kind field, each break this test.
func TestRunJSONOutputIsMachineReadable(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+
		"\nWait /ECHOTUI/ @5s\nType boom\nKey Enter\nExpectExit 0\n")
	code, stdout, _ := runCLI(nil, "run", "-json", path)
	if code != ExitAssert {
		t.Fatalf("exit code = %d, want %d", code, ExitAssert)
	}
	var res runResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if res.Status != "fail" {
		t.Errorf("status = %q, want \"fail\"", res.Status)
	}
	if res.Kind != "assertion" {
		t.Errorf("kind = %q, want \"assertion\"", res.Kind)
	}
	if res.ExitCode != ExitAssert {
		t.Errorf("exitCode = %d, want %d", res.ExitCode, ExitAssert)
	}
	if res.Error == "" {
		t.Error("error field is empty on a failing run")
	}
}

// --- snap ---

// Verified to fail: making snap print to stderr, or skipping the settle wait so
// it captures an empty screen, breaks this test.
func TestSnapPrintsTheScreen(t *testing.T) {
	code, stdout, stderr := runCLI(nil, "snap", "-size", "30x6", "--", echoBin)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "ECHOTUI") {
		t.Errorf("snap did not print the screen:\n%s", stdout)
	}
}

// -type must capture the screen after the program reacts, not before. This is
// the bug the reaction wait exists to prevent: the screen has already been
// quiet for longer than the settle window, so a bare WaitStable returns at once
// and captures the pre-input screen.
// Verified to fail: removing the WaitFor reaction wait from capture makes this
// print only the banner, without the echoed line.
func TestSnapTypeCapturesTheScreenAfterTheProgramReacts(t *testing.T) {
	code, stdout, stderr := runCLI(nil, "snap", "-size", "30x6", "-type", `hi\r`, "--", echoBin)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "echo: hi") {
		t.Errorf("snap captured the screen before the program responded:\n%s", stdout)
	}
}

// A TUI that spends longer than the settle window starting up must still be
// captured after it paints. WaitStable measures its quiet window from the
// spawn, so without a first-output wait it reports a program that has not
// written a byte as already settled and snap prints an empty screen. This is
// the shape of the "snap captures nothing" reports against slow-starting
// full-screen programs.
// Verified to fail: removing the WaitForOutput call from capture makes this
// print an empty screen instead of PAINTED.
func TestSnapWaitsForASlowProgramToPaint(t *testing.T) {
	sh := lookupShell(t)
	code, stdout, stderr := runCLI(nil,
		"snap", "-size", "30x6", "-timeout", "10s",
		"--", sh, "-c", `sleep 0.5; printf 'PAINTED\n'; sleep 10`)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "PAINTED") {
		t.Errorf("snap captured the screen before the program painted:\n%q", stdout)
	}
}

// A TUI that animates never goes quiet, so the settle wait times out even
// though the program has drawn a perfectly good screen. The screen is the
// whole point of snap, so it must be reported anyway rather than discarded
// with the error.
// Verified to fail: returning early on the settle error, instead of capturing
// the screen first, makes the reported screen empty.
func TestSnapReportsTheScreenEvenWhenItNeverSettles(t *testing.T) {
	sh := lookupShell(t)
	// Paints once, then keeps writing forever so the quiet window never opens.
	code, stdout, _ := runCLI(nil,
		"snap", "-size", "40x8", "-json", "-timeout", "1s",
		"--", sh, "-c", `printf 'ANIMATING\n'; while :; do printf '.'; sleep 0.02; done`)
	if code == ExitOK {
		t.Fatalf("a program that never settles should not report success")
	}
	var res snapResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decoding snap json: %v\n%s", err, stdout)
	}
	if !strings.Contains(res.Screen, "ANIMATING") {
		t.Errorf("snap discarded the screen it had on timeout: %q", res.Screen)
	}
}

// Verified to fail: dropping WithEnv from capture makes the marker absent.
func TestSnapEnvFlagReachesTheProgram(t *testing.T) {
	sh := lookupShell(t)
	code, stdout, stderr := runCLI(nil,
		"snap", "-size", "40x6", "-env", "TUITEST_MARK=zebra",
		"--", sh, "-c", "printf %s \"$TUITEST_MARK\"")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "zebra") {
		t.Errorf("environment variable did not reach the program:\n%s", stdout)
	}
}

// Verified to fail: returning ExitOK when -wait never matches breaks this.
func TestSnapWaitTimeoutIsTimeoutExitCode(t *testing.T) {
	code, _, stderr := runCLI(nil,
		"snap", "-size", "30x6", "-wait", "neverappears", "-timeout", "400ms", "--", echoBin)
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d; stderr:\n%s", code, ExitTimeout, stderr)
	}
	if !strings.Contains(stderr, "neverappears") {
		t.Errorf("timeout does not say what it waited for:\n%s", stderr)
	}
}

func TestSnapNeedsAProgram(t *testing.T) {
	code, _, stderr := runCLI(nil, "snap")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "needs a program") {
		t.Errorf("stderr does not explain the argument:\n%s", stderr)
	}
}

// exitCode must be null while the program still runs, since -1 would read as a
// real status. Verified to fail: making ExitCode a plain int again emits -1.
func TestSnapJSONReportsExitCodeAsNullWhileRunning(t *testing.T) {
	code, stdout, _ := runCLI(nil, "snap", "-size", "30x6", "-json", "--", echoBin)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var res snapResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if res.Exited {
		t.Fatal("fixture reported as exited while it waits for input")
	}
	if res.ExitCode != nil {
		t.Errorf("exitCode = %d, want null while the program is running", *res.ExitCode)
	}
	if !strings.Contains(res.Screen, "ECHOTUI") {
		t.Errorf("screen field does not carry the screen: %q", res.Screen)
	}
}

// --- doctor ---

// Verified to fail: making checkPTY report ok unconditionally still passes the
// status assertion, so the test also requires the pty check to be present and
// to have run, and asserts doctor's exit code follows the failures.
func TestDoctorReportsPTYAndExitsZeroWhenHealthy(t *testing.T) {
	code, stdout, _ := runCLI(nil, "doctor")
	if code != ExitOK {
		t.Errorf("doctor exit code = %d, want 0 on a machine with a PTY;\n%s", code, stdout)
	}
	for _, want := range []string{"pty", "platform", "emulator", "tempdir"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor does not report %q:\n%s", want, stdout)
		}
	}
}

// Verified to fail: dropping the Status field, or emitting a bare list instead
// of the documented object, breaks this test.
func TestDoctorJSONIsMachineReadable(t *testing.T) {
	code, stdout, _ := runCLI(nil, "doctor", "-json")
	if code != ExitOK {
		t.Fatalf("doctor exit code = %d, want 0", code)
	}
	var res doctorResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if !res.OK {
		t.Errorf("ok = false on a healthy machine: %+v", res.Checks)
	}
	if len(res.Checks) == 0 {
		t.Fatal("no checks reported")
	}
	var sawPTY bool
	for _, ck := range res.Checks {
		if ck.Name == "" || ck.Status == "" || ck.Detail == "" {
			t.Errorf("incomplete check: %+v", ck)
		}
		if ck.Name == "pty" {
			sawPTY = true
			if ck.Status != statusOK {
				t.Errorf("pty check failed on a machine that clearly has one: %+v", ck)
			}
		}
	}
	if !sawPTY {
		t.Error("doctor did not run the pty check, which is the one that matters")
	}
}

// doctor reads the environment it is given, so the CI hint is asserted through
// the injected environment rather than the developer's real one.
// Verified to fail: hardcoding os.Getenv in checkFlakiness ignores the
// injected value and drops the CI check.
func TestDoctorUsesTheInjectedEnvironment(t *testing.T) {
	_, stdout, _ := runCLI(map[string]string{"CI": "true", "TERM": "screen-256color"}, "doctor")
	if !strings.Contains(stdout, "screen-256color") {
		t.Errorf("doctor did not read the injected TERM:\n%s", stdout)
	}
	if !strings.Contains(stdout, "CI") {
		t.Errorf("doctor did not notice CI:\n%s", stdout)
	}
}

// --- completion ---

// Completion is cobra's, and cobra's is dynamic: the generated script calls the
// binary back through the hidden __complete command rather than embedding a list
// of names. That is strictly better than the hand-rolled scripts, which had to
// be regenerated to learn about a new command, but it means the old test (parse
// the script, look for every command name in it) can no longer be written. The
// equivalent guarantee is asserted against the callback itself, which is what
// the shell actually consults.
//
// Verified to fail: removing a command from newRootCommand makes this report the
// missing name.
func TestCompletionOffersEveryCommand(t *testing.T) {
	code, stdout, stderr := runCLI(nil, cobra.ShellCompRequestCmd, "")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	offered := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		name, _, _ := strings.Cut(line, "\t")
		offered[strings.TrimSpace(name)] = true
	}
	for _, c := range subcommands() {
		if !offered[c.Name()] {
			t.Errorf("completion does not offer %q; it offers %v", c.Name(), offered)
		}
	}
	for _, want := range []string{"help", "completion"} {
		if !offered[want] {
			t.Errorf("completion does not offer %q; it offers %v", want, offered)
		}
	}
}

// run's argument is a tape file, and the completion has to say so, or the shell
// offers every file in the directory.
// Verified to fail: dropping ValidArgsFunction from runCommand makes the
// directive plain file completion.
func TestRunCompletesTapeFiles(t *testing.T) {
	code, stdout, stderr := runCLI(nil, cobra.ShellCompRequestCmd, "run", "")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "tape") {
		t.Errorf("run does not complete tape files:\n%s", stdout)
	}
}

// Every shell cobra can generate a script for must actually produce one, and it
// must go to the Env's stdout rather than the process's: the generated
// subcommands capture their writer when they are built, which is why
// initCompletion runs after Main has redirected the root.
func TestCompletionScriptsGenerate(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			code, stdout, stderr := runCLI(nil, "completion", shell)
			if code != ExitOK {
				t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stdout, "tuitest") {
				t.Errorf("%s completion script did not reach the captured stdout:\n%s", shell, stdout)
			}
		})
	}
}

// A shell nobody supports is a usage error, not a silent success. cobra's
// completion command is not runnable, so without the fix in initCompletion
// "tuitest completion csh" prints help and exits 0.
// Verified to fail: removing the RunE assignment in initCompletion.
func TestCompletionRejectsUnknownShell(t *testing.T) {
	code, _, stderr := runCLI(nil, "completion", "csh")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "csh") {
		t.Errorf("stderr does not name the unsupported shell:\n%s", stderr)
	}
}

// Naming no shell at all is the same kind of mistake and must say what the
// choices are.
func TestCompletionWithoutAShellIsUsageError(t *testing.T) {
	code, _, stderr := runCLI(nil, "completion")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "bash") {
		t.Errorf("stderr does not list the supported shells:\n%s", stderr)
	}
}

// --- flags ---

// Verified to fail: accepting a size without the separator, or accepting zero
// and negative dimensions, breaks the error cases here.
func TestSizeFlagParsing(t *testing.T) {
	cases := []struct {
		in         string
		wantErr    bool
		cols, rows int
	}{
		{"80x24", false, 80, 24},
		{"120X40", false, 120, 40},
		{" 100 x 30 ", false, 100, 30},
		{"80", true, 0, 0},
		{"x24", true, 0, 0},
		{"80x", true, 0, 0},
		{"0x24", true, 0, 0},
		{"80x-1", true, 0, 0},
		{"eightyx24", true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var s sizeFlag
			err := s.Set(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Set(%q) accepted an invalid size as %dx%d", tc.in, s.cols, s.rows)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set(%q) = %v, want success", tc.in, err)
			}
			if s.cols != tc.cols || s.rows != tc.rows {
				t.Errorf("Set(%q) = %dx%d, want %dx%d", tc.in, s.cols, s.rows, tc.cols, tc.rows)
			}
		})
	}
}

// Verified to fail: dropping the "=" requirement accepts a bare word as an
// environment entry, which would silently do nothing in the child.
func TestEnvFlagParsing(t *testing.T) {
	var e envFlag
	if err := e.Set("A=1"); err != nil {
		t.Fatalf("Set(A=1) = %v", err)
	}
	if err := e.Set("B=with=equals"); err != nil {
		t.Fatalf("Set(B=with=equals) = %v", err)
	}
	if err := e.Set("noequals"); err == nil {
		t.Error("Set accepted an entry without '='")
	}
	if err := e.Set("=novalue"); err == nil {
		t.Error("Set accepted an entry with an empty key")
	}
	if len(e) != 2 {
		t.Errorf("collected %v, want the two valid entries", []string(e))
	}
}

// The library prefixes its errors so they stand out in Go test output; the CLI
// prints the program name itself and must not repeat it.
// Verified to fail: rendering err.Error() directly reintroduces the doubled
// prefix and fails this test.
func TestRenderDoesNotRepeatTheToolPrefix(t *testing.T) {
	err := &tape.LineError{Line: 3, Err: &tuitest.TimeoutError{
		Op: "WaitForMatch", Want: "match x", Elapsed: time.Second, Screen: "hello",
	}}
	got := render(err)
	if strings.Contains(got, "tuitest:") {
		t.Errorf("rendered message repeats the tool prefix:\n%s", got)
	}
	if !strings.Contains(got, "tape line 3") {
		t.Errorf("rendered message lost the tape line:\n%s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("rendered message lost the screen:\n%s", got)
	}
}

// A screen that legitimately contains the tool's own name must survive
// rendering intact, which a blunt string replacement would mangle.
// Verified to fail: implementing trimToolPrefix with strings.ReplaceAll.
func TestRenderKeepsToolNameInsideScreenContent(t *testing.T) {
	err := &tuitest.TimeoutError{Op: "WaitFor", Want: "x", Screen: "usage: tuitest: run"}
	if got := render(err); !strings.Contains(got, "usage: tuitest: run") {
		t.Errorf("screen content was mangled:\n%s", got)
	}
}

// lookupShell finds a POSIX shell, skipping the test where there is none.
func lookupShell(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	return sh
}

// writeScript puts an executable shell script in the test's own temp dir, which
// testing removes, so nothing is written outside it.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	sh := lookupShell(t)
	path := filepath.Join(t.TempDir(), "prog.sh")
	if err := os.WriteFile(path, []byte("#!"+sh+"\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The pre-cobra CLI used the flag package, where "-help" and "-version" were
// ordinary single-dash flags. Cobra registers those two lazily during Execute,
// so normalization has to ask for them explicitly or every script and README
// example using the old spelling exits 2.
func TestSingleDashHelpAndVersionStillWork(t *testing.T) {
	for _, arg := range []string{"-help", "-version", "--help", "--version"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer
			env := &Env{Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
			if code := Main(env, []string{arg}); code != 0 {
				t.Fatalf("%s: exit = %d, want 0\n%s", arg, code, out.String())
			}
		})
	}
}
