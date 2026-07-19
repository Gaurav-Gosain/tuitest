package tuitest_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

var echoBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tuitest-fixture-")
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
	os.Exit(code)
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
