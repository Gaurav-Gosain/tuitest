package tuitest_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

var echoBin string

const (
	echoFixturePrefix  = "tuitest-fixture-"
	stateFixturePrefix = "tuitest-state-fixture-"
)

func TestMain(m *testing.M) {
	// A run killed by a timeout or a panic never reaches the cleanup below, so
	// sweep what earlier runs left behind first. Only directories older than an
	// hour go, so a concurrent run of this package keeps its own fixture.
	sweepStaleFixtures(echoFixturePrefix, time.Hour)
	sweepStaleFixtures(stateFixturePrefix, time.Hour)

	dir, err := os.MkdirTemp("", echoFixturePrefix)
	if err != nil {
		panic(err)
	}
	echoBin = filepath.Join(dir, "echotui")
	build := exec.Command("go", "build", "-o", echoBin, "./testdata/echotui")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building echotui fixture: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	// The buggytui fixture is built lazily by the tests that need it, so its
	// directory is only set when one of them ran.
	if buggyDir != "" {
		_ = os.RemoveAll(buggyDir)
	}
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

func startEcho(t *testing.T, opts ...tuitest.Option) *tuitest.Terminal {
	t.Helper()
	return tuitest.StartT(t, []string{echoBin}, opts...)
}

func TestBannerAppears(t *testing.T) {
	t.Parallel()
	term := startEcho(t, tuitest.WithSize(40, 10))
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText(">", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestEchoRoundTrip(t *testing.T) {
	t.Parallel()
	term := startEcho(t, tuitest.WithSize(40, 10))
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys("hello", tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("echo: hello", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForMatchLastLine(t *testing.T) {
	t.Parallel()
	term := startEcho(t, tuitest.WithSize(40, 10))
	if err := term.WaitForMatch(regexp.MustCompile(`^> ?$`), tuitest.ScopeLastLine, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys("world", tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	// After echo, the prompt is again the last line.
	if err := term.WaitForMatch(regexp.MustCompile(`echo: world`), tuitest.ScopeScreen, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitStable(t *testing.T) {
	t.Parallel()
	term := startEcho(t, tuitest.WithSize(40, 10), tuitest.WithStabilizeInterval(100*time.Millisecond))
	if err := term.WaitStable(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := term.Snapshot(); !contains(got, "ECHOTUI") {
		t.Fatalf("after stable, snapshot missing banner:\n%s", got)
	}
}

func TestExitCodeClean(t *testing.T) {
	t.Parallel()
	term := startEcho(t)
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys("quit", tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	code, err := term.Wait(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestExitCodeNonZero(t *testing.T) {
	t.Parallel()
	term := startEcho(t)
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys("boom", tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	code, err := term.Wait(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestWaitReturnsClosedError(t *testing.T) {
	t.Parallel()
	term := startEcho(t)
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys("quit", tuitest.Enter); err != nil {
		t.Fatal(err)
	}
	// Waiting for text that never comes should surface a ClosedError, not hang
	// until timeout, once the child exits.
	err := term.WaitForText("this text never appears", 5*time.Second)
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := err.(*tuitest.ClosedError); !ok {
		t.Fatalf("expected *ClosedError, got %T: %v", err, err)
	}
	// Callers should be able to branch on the kind of failure without knowing
	// the concrete type.
	if !errors.Is(err, tuitest.ErrChildExited) {
		t.Errorf("errors.Is(err, ErrChildExited) = false for %v", err)
	}
	if errors.Is(err, tuitest.ErrTimeout) {
		t.Errorf("a child-exit failure should not match ErrTimeout: %v", err)
	}
}

func TestTimeoutErrorHasScreen(t *testing.T) {
	t.Parallel()
	term := startEcho(t)
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	err := term.WaitForText("never-shows-up", 300*time.Millisecond)
	te, ok := err.(*tuitest.TimeoutError)
	if !ok {
		t.Fatalf("expected *TimeoutError, got %T: %v", err, err)
	}
	if !contains(te.Screen, "ECHOTUI") {
		t.Fatalf("timeout error screen dump missing banner:\n%s", te.Screen)
	}
	// The mirrored I/O tail is the other half of what makes a timeout report
	// actionable, and nothing else asserted that it is ever populated.
	if !contains(te.TailLog, "ECHOTUI") {
		t.Errorf("timeout error is missing the mirrored I/O tail:\n%q", te.TailLog)
	}
	if !contains(err.Error(), "last I/O") {
		t.Errorf("timeout message does not include the I/O section:\n%s", err)
	}
	if !errors.Is(err, tuitest.ErrTimeout) {
		t.Errorf("errors.Is(err, ErrTimeout) = false for %v", err)
	}
}

func TestResize(t *testing.T) {
	t.Parallel()
	term := startEcho(t, tuitest.WithSize(40, 10))
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(60, 20); err != nil {
		t.Fatal(err)
	}
	sc := term.Screen()
	cols, rows := sc.Size()
	if cols != 60 || rows != 20 {
		t.Fatalf("size after resize = %dx%d, want 60x20", cols, rows)
	}
}

func TestSemanticDisabledError(t *testing.T) {
	t.Parallel()
	term := startEcho(t)
	err := term.WaitForPrompt(time.Second)
	if err == nil {
		t.Fatal("WaitForPrompt without WithSemanticMarkers should error")
	}
	if !errors.Is(err, tuitest.ErrSemanticMarkers) {
		t.Errorf("errors.Is(err, ErrSemanticMarkers) = false for %v", err)
	}
	if _, ok := term.LastCommandExit(); ok {
		t.Error("LastCommandExit should report false without WithSemanticMarkers")
	}
}

// TestHeavyOutputStabilizes exercises the harness under a large, fast output
// burst: the VT pump, the wait machinery, and stabilization must handle it
// without crashing or deadlocking. Running under -race is the point. This is the
// deterministic, harness-level analogue of the tuios issue-19 stress class.
func TestHeavyOutputStabilizes(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	term := tuitest.StartT(t, []string{"sh", "-c", "seq 1 20000"}, tuitest.WithSize(80, 24))
	if err := term.WaitStable(20 * time.Second); err != nil {
		t.Fatalf("did not stabilize under heavy output: %v", err)
	}
	// After all output, the final visible line should reflect the end of the run.
	if err := term.WaitForText("20000", 5*time.Second); err != nil {
		t.Fatalf("final output missing: %v", err)
	}
	code, exited := term.ExitCode()
	if !exited || code != 0 {
		t.Fatalf("exit = (%d, %v), want (0, true)", code, exited)
	}
}

func TestGoldenBanner(t *testing.T) {
	t.Parallel()
	term := startEcho(t, tuitest.WithSize(20, 4))
	if err := term.WaitForText("ECHOTUI", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitStable(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	term.AssertGolden(t, "banner")
	term.AssertGoldenStyled(t, "banner-styled")
}

func contains(hay, needle string) bool {
	return len(needle) == 0 || (len(hay) >= len(needle) && indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestTerminalQueryAnswered checks that a query the child issues is answered
// through the real PTY path. A terminal that never replies leaves programs
// that probe before drawing (background colour detection, cursor position
// reports) stuck in a retry loop, which shows up as a blank capture.
func TestTerminalQueryAnswered(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("stty"); err != nil {
		t.Skip("stty not available")
	}
	// Ask for the cursor position, then read whatever comes back within the
	// stty timeout and print it with the ESC rendered as a caret so it can be
	// matched on screen. Raw mode is needed because the reply carries no
	// newline to release a canonical read.
	const script = `stty raw -echo min 0 time 30; printf '\033[6n'; ` +
		`reply=$(dd bs=32 count=1 2>/dev/null | tr -d '\033'); ` +
		`stty sane; printf 'REPLY[%s]\r\n' "$reply"`
	term := tuitest.StartT(t, []string{"sh", "-c", script}, tuitest.WithSize(40, 10))
	if err := term.WaitForMatch(regexp.MustCompile(`REPLY\[\[\d+;\d+R\]`), tuitest.ScopeScreen, 10*time.Second); err != nil {
		t.Fatalf("cursor position query went unanswered: %v\n%s", err, term.Snapshot())
	}
}
