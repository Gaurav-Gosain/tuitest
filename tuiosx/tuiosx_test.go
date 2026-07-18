package tuiosx_test

import (
	"os"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tuiosx"
)

// These acceptance tests drive the real tuios binary and are gated behind the
// TUITEST_TUIOS environment variable, because spawning a full multiplexer (with
// its daemon and pane processes) is heavier and less deterministic than the
// fixture tests. Run them with:
//
//	TUITEST_TUIOS=1 go test -race ./tuiosx/...
func gate(t *testing.T) {
	if os.Getenv("TUITEST_TUIOS") == "" {
		t.Skip("set TUITEST_TUIOS=1 to run tuios acceptance tests")
	}
}

// TestTuiosBoots proves the harness spawns tuios in a headless PTY and captures
// its rendered welcome frame.
func TestTuiosBoots(t *testing.T) {
	gate(t)
	t.Parallel()
	term := tuiosx.StartTuios(t, tuitest.WithSize(120, 40))
	if err := term.WaitForText("Terminal UI Operating System", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, exited := term.ExitCode(); exited {
		t.Fatal("tuios exited during boot")
	}
}

// TestNewWindowCreates opens the first window from the welcome screen ('n') and
// verifies the frame changes and tuios stays alive. It stabilizes rather than
// sleeps.
func TestNewWindowCreates(t *testing.T) {
	gate(t)
	t.Parallel()
	term := tuiosx.StartTuios(t, tuitest.WithSize(120, 40))
	if err := term.WaitForText("Terminal UI Operating System", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitStable(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	before := term.Snapshot()

	if err := term.SendKeys("n"); err != nil {
		t.Fatal(err)
	}
	// WaitStable only proves quiescence, not that the (delayed) window actually
	// appeared, so wait for the frame to differ from the welcome screen.
	if err := term.WaitFor(func(s tuitest.Screen) bool { return s.Text() != before }, 5*time.Second); err != nil {
		t.Fatal("screen did not change after creating a window: " + err.Error())
	}
	if _, exited := term.ExitCode(); exited {
		t.Fatal("tuios exited after creating a window")
	}
}
