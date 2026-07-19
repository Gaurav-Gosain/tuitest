package fuzz_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/fuzz"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// buggyBin is the fixture TUI with deliberate bugs, built once for the package.
var buggyBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tuitest-fuzz-fixture-")
	if err != nil {
		panic(err)
	}
	buggyBin = filepath.Join(dir, "buggytui")
	build := exec.Command("go", "build", "-o", buggyBin, "../testdata/buggytui")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building buggytui fixture: " + err.Error())
	}
	code := m.Run()
	// Remove the build directory whatever the outcome, so a failing run does
	// not leave binaries behind.
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// argvFor returns the fixture command line for one deliberate bug.
func argvFor(bug string) []string {
	return []string{buggyBin, "-bug", bug}
}

// baseOptions are shared by the end-to-end tests: small, bounded, and quiet, so
// the suite stays fast and cannot run away.
func baseOptions(t *testing.T, bug string) fuzz.Options {
	t.Helper()
	return fuzz.Options{
		Argv:       argvFor(bug),
		Iterations: 20,
		Gen: fuzz.Config{
			Cols:          80,
			Rows:          24,
			ActionsPerRun: 40,
		},
		Limits: fuzz.Limits{
			// Well under the default, to keep the suite quick. The fixture
			// either answers immediately or never.
			HangAfter: 1500 * time.Millisecond,
		},
		StopOnFirst:   true,
		Shrink:        true,
		ShrinkBudget:  120,
		SettleTimeout: 750 * time.Millisecond,
		// Corpus is left empty so no test writes outside its temp dir unless it
		// sets one explicitly.
	}
}

// runFuzz runs a session under a deadline derived from the test's own, so a
// wedged fixture can never hang the suite.
func runFuzz(t *testing.T, opts fuzz.Options) *fuzz.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	res, err := fuzz.Run(ctx, opts)
	if err != nil {
		t.Fatalf("fuzz.Run: %v", err)
	}
	return res
}

func findFailure(res *fuzz.Result, kind fuzz.FailureKind) *fuzz.Failure {
	for _, f := range res.Failures {
		if f.Kind == kind {
			return f
		}
	}
	return nil
}

func summarise(res *fuzz.Result) string {
	if len(res.Failures) == 0 {
		return "no failures"
	}
	var b strings.Builder
	for _, f := range res.Failures {
		b.WriteString(string(f.Kind) + ": " + f.Detail + "\n")
	}
	return b.String()
}

// The control. A well-behaved program must produce no findings at all, across
// several seeds. This is the test that guards against the detectors becoming
// noise, and it is the one that caught two real false positives during
// development: an idle program being reported as hung, and silence accumulated
// while nothing was being sent counting toward the hang bound.
//
// Verified to fail on broken code: dropping the minIgnoredInputs gate from
// checkHang makes seeds report spurious hangs here.
func TestWellBehavedProgramProducesNoFindings(t *testing.T) {
	t.Parallel()

	for _, seed := range []uint64{1, 2, 3, 7} {
		t.Run("seed"+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			opts := baseOptions(t, "none")
			opts.Seed = seed
			opts.Iterations = 8
			opts.StopOnFirst = false

			res := runFuzz(t, opts)
			if len(res.Failures) != 0 {
				t.Fatalf("the well-behaved fixture must produce no findings, got:\n%s", summarise(res))
			}
		})
	}
}

// Verified to fail on broken code: making ExitStatus.Crashed always return
// false makes the fuzzer miss the panic entirely and this test fails.
func TestFindsPanicAndMinimisesToTheTriggeringKey(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "panic-on-key")
	opts.Seed = 1
	// The bug is bound to one key out of a large space, so this needs enough
	// iterations to be sure of sending it.
	opts.Iterations = 40

	res := runFuzz(t, opts)

	f := findFailure(res, fuzz.FailCrash)
	if f == nil {
		t.Fatalf("expected a crash finding, got:\n%s", summarise(res))
	}

	// The reproduction must be minimal and must contain the key that causes it.
	if !containsKey(f.Commands, "F5") {
		t.Fatalf("the reproduction must contain the key that triggers the panic:\n%s", tape.Format(f.Commands))
	}
	if len(f.Commands) > 6 {
		t.Fatalf("reproduction is %d commands, expected minimisation to a handful:\n%s",
			len(f.Commands), tape.Format(f.Commands))
	}
	if !f.Verified {
		t.Error("the minimised reproduction did not reproduce on confirmation")
	}
	if f.Original <= len(f.Commands) {
		t.Errorf("Original=%d and minimised=%d: minimisation should have reduced the input",
			f.Original, len(f.Commands))
	}
}

// Verified to fail on broken code: making TermState.Dirty always return false
// makes the fuzzer miss the unrestored terminal and this test fails.
func TestFindsTerminalLeftInABadState(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "dirty-exit")
	opts.Seed = 5
	opts.Iterations = 30

	res := runFuzz(t, opts)

	f := findFailure(res, fuzz.FailDirtyExit)
	if f == nil {
		t.Fatalf("expected a dirty-terminal finding, got:\n%s", summarise(res))
	}
	// The fixture leaves all three of these set, and the message is what tells
	// an author what to fix.
	for _, want := range []string{"alternate screen", "mouse tracking", "cursor still hidden"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("detail should mention %q, got %q", want, f.Detail)
		}
	}
	if !f.Verified {
		t.Error("the minimised reproduction did not reproduce on confirmation")
	}
}

// Verified to fail on broken code: removing the Resize handling from
// monitor.noteCommand, or making checkHang return nil unconditionally, makes
// the fuzzer miss the wedge and this test fails.
func TestFindsHangAndMinimisesToTheTriggeringResize(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "hang-on-narrow")
	opts.Seed = 3
	opts.Iterations = 12

	res := runFuzz(t, opts)

	f := findFailure(res, fuzz.FailHang)
	if f == nil {
		t.Fatalf("expected a hang finding, got:\n%s", summarise(res))
	}
	// The wedge only happens at a single column, so a correct reproduction must
	// resize to one.
	if !containsNarrowResize(f.Commands) {
		t.Fatalf("the reproduction must contain the resize that wedges the program:\n%s",
			tape.Format(f.Commands))
	}
	if len(f.Commands) > 12 {
		t.Fatalf("reproduction is %d commands, expected minimisation:\n%s",
			len(f.Commands), tape.Format(f.Commands))
	}
}

// A reproduction is only useful if it is a tape the parser accepts, so the
// generated text must survive a round trip.
//
// Verified to fail on broken code: making Quote return its argument unquoted
// makes the emitted Raw lines unparseable and this test fails.
func TestReproductionTapeParsesBack(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "panic-on-key")
	opts.Seed = 1
	opts.Iterations = 40

	res := runFuzz(t, opts)
	f := findFailure(res, fuzz.FailCrash)
	if f == nil {
		t.Fatalf("expected a crash finding, got:\n%s", summarise(res))
	}

	text := fuzz.TapeFor(f)
	parsed, err := tape.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("the generated reproduction does not parse: %v\n%s", err, text)
	}
	if len(parsed) != len(f.Commands) {
		t.Fatalf("reproduction parsed to %d commands, want %d:\n%s", len(parsed), len(f.Commands), text)
	}
	// The header must carry the information needed to act on the finding.
	for _, want := range []string{"crash", "seed", "replay with"} {
		if !strings.Contains(text, want) {
			t.Errorf("reproduction header should mention %q:\n%s", want, text)
		}
	}
}

// The corpus turns findings into regression tests: a case written on one run
// must be replayed and still reported on the next.
//
// Verified to fail on broken code: making replayCorpus return nil without
// reading the directory makes this test fail, because the known case is never
// re-reported.
func TestCorpusIsWrittenAndReplayedAsARegression(t *testing.T) {
	t.Parallel()

	// t.TempDir is removed automatically, so nothing is written outside it.
	corpus := t.TempDir()

	opts := baseOptions(t, "dirty-exit")
	opts.Seed = 5
	opts.Iterations = 30
	opts.Corpus = corpus

	first := runFuzz(t, opts)
	if findFailure(first, fuzz.FailDirtyExit) == nil {
		t.Fatalf("expected a dirty-terminal finding on the first run, got:\n%s", summarise(first))
	}

	entries, err := os.ReadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var tapes []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tape" {
			tapes = append(tapes, e.Name())
		}
	}
	if len(tapes) == 0 {
		t.Fatalf("no reproduction was written to the corpus directory")
	}

	// Replay only: no new generation, so anything reported must have come from
	// the corpus entry written above.
	replay := opts
	replay.Iterations = 0
	replay.Duration = time.Nanosecond
	replay.Shrink = false

	second := runFuzz(t, replay)
	if findFailure(second, fuzz.FailDirtyExit) == nil {
		t.Fatalf("the corpus entry should still reproduce on replay, got:\n%s", summarise(second))
	}
}

// A session must be reproducible from its seed, or none of the reported seeds
// are actionable.
//
// Verified to fail on broken code: seeding the generator from the clock instead
// of the run seed makes the two sessions diverge and this test fails.
func TestSameSeedProducesTheSameSession(t *testing.T) {
	t.Parallel()

	run := func() string {
		opts := baseOptions(t, "panic-on-key")
		opts.Seed = 99
		opts.Iterations = 40
		res := runFuzz(t, opts)
		f := findFailure(res, fuzz.FailCrash)
		if f == nil {
			return "none"
		}
		return tape.Format(f.Commands)
	}

	a, b := run(), run()
	if a != b {
		t.Fatalf("the same seed produced different reproductions:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
	if a == "none" {
		t.Fatal("expected the session to find the planted crash")
	}
}

func containsKey(cmds []tape.Command, want string) bool {
	for _, c := range cmds {
		if c.Kind != tape.KindKey {
			continue
		}
		for _, k := range c.Keys {
			if k == want {
				return true
			}
		}
	}
	return false
}

func containsNarrowResize(cmds []tape.Command) bool {
	for _, c := range cmds {
		if c.Kind == tape.KindResize && c.Cols <= 1 {
			return true
		}
	}
	return false
}

// countLiveFixtures reports how many copies of the fixture binary are running.
// It reads /proc directly rather than shelling out to pgrep, so it does not
// depend on tools being installed and cannot itself spawn anything.
func countLiveFixtures(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("no /proc on this platform: %v", err)
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // not a pid directory
		}
		target, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue // gone, or not ours to inspect
		}
		if target == buggyBin {
			n++
		}
	}
	return n
}

// A fuzz session spawns one program per iteration plus one per shrink replay,
// which is hundreds of processes. Every one of them must be reaped. A harness
// that leaks them fills the machine, and this project has been bitten by that
// before, so it is asserted rather than assumed.
//
// Verified to fail on broken code: removing the `defer p.Close()` from drive
// leaves dozens of fixture processes alive and this test fails.
func TestSessionLeavesNoProcessesBehind(t *testing.T) {
	// Deliberately not parallel: it counts processes globally, so a concurrent
	// test spawning the same fixture would make the count meaningless.
	before := countLiveFixtures(t)

	opts := baseOptions(t, "hang-on-narrow")
	opts.Seed = 3
	opts.Iterations = 6
	runFuzz(t, opts)

	// Teardown signals the process group and waits for it, but the kernel may
	// take a moment to clear the /proc entry of a reaped process.
	deadline := time.Now().Add(10 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		after = countLiveFixtures(t)
		if after <= before {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d fixture processes were still running after the session (was %d before); "+
		"a fuzz run must reap every program it spawns", after, before)
}

// A program that cannot be started must be reported as an error, not as a
// clean run. Silently succeeding here would let a typo in the command line
// look like a program with no bugs, which is the most misleading possible
// outcome for a tool whose entire job is finding bugs.
//
// Verified to fail on broken code: swallowing the spawn error in
// driveReportingSpawn and returning (nil, nil) makes Run report success and
// this test fails.
func TestUnstartableProgramIsAnError(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "none")
	opts.Argv = []string{filepath.Join(t.TempDir(), "does-not-exist")}
	opts.Iterations = 3

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := fuzz.Run(ctx, opts)
	if err == nil {
		t.Fatalf("a program that cannot be started must be an error, got result: %+v", res)
	}
	if !strings.Contains(err.Error(), "cannot start") {
		t.Errorf("the error should say the program could not be started, got %q", err)
	}
}
