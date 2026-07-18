package tuios_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// TestFloodWithResize reproduces the *shape* of the issue-19 regression: a pane
// producing a torrent of output while the window is resized repeatedly. That
// combination used to race the VT read path and crash tuios. The harness's job
// here is to catch such a crash: if tuios dies (panic -> process exit -> PTY
// EOF) at any point during the storm, the assertions below fail with the last
// rendered screen attached.
//
// This deliberately does NOT wait for the flood to drain. `seq 1 2000000`
// emits ~2M lines and the VT emulator interprets only a few thousand lines a
// second, so full drain would take minutes. The test only needs tuios to keep
// running while output floods and SIGWINCH storms in; it stops the flood with
// Ctrl+C and lets the harness tear the process group down.
func TestFloodWithResize(t *testing.T) {
	t.Parallel()
	term := startTuios(t, tuitest.WithSize(120, 40))

	// Boot + create a window + enter terminal mode.
	if err := term.WaitForText(welcomeText, 10*time.Second); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if err := term.SendKeys("n"); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitFor(func(s tuitest.Screen) bool {
		return !strings.Contains(s.Text(), welcomeHint)
	}, 5*time.Second); err != nil {
		t.Fatalf("window did not appear: %v", err)
	}
	if err := term.SendKeys("i"); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("Terminal Mode", 5*time.Second); err != nil {
		t.Fatalf("did not enter terminal mode: %v", err)
	}
	settlePastInsertGuard()

	// Kick off the flood inside the pane's shell.
	if err := term.SendKeys("seq 1 2000000", tuitest.Enter); err != nil {
		t.Fatalf("start flood: %v", err)
	}

	// Confirm the flood is actually running before we start resizing, so the
	// storm overlaps real output rather than an idle prompt.
	if err := term.WaitForText("100", 5*time.Second); err != nil {
		t.Fatalf("flood did not start producing output: %v", err)
	}

	// Resize storm: bounce the PTY between sizes while output pours in. Each
	// Resize sends SIGWINCH, forcing tuios to re-tile and re-read the pane -
	// exactly the concurrency the regression lived in.
	sizes := [][2]int{{120, 40}, {90, 30}, {140, 50}, {80, 24}, {110, 45}, {100, 20}}
	deadline := time.Now().Add(5 * time.Second)
	iterations := 0
	for i := 0; time.Now().Before(deadline); i++ {
		w, h := sizes[i%len(sizes)][0], sizes[i%len(sizes)][1]
		if err := term.Resize(w, h); err != nil {
			// A failed resize means the PTY is gone: tuios died mid-storm.
			t.Fatalf("resize %dx%d failed after %d iterations (tuios likely crashed): %v\n%s",
				w, h, iterations, err, term.Snapshot())
		}
		if code, exited := term.ExitCode(); exited {
			t.Fatalf("tuios exited (code %d) during flood+resize storm after %d iterations\n%s",
				code, iterations, term.Snapshot())
		}
		iterations++
		time.Sleep(40 * time.Millisecond)
	}
	t.Logf("survived %d resize iterations under flood", iterations)

	// Stop the flood and confirm tuios is still alive and responsive to input
	// (Ctrl+C is delivered through the same PTY the storm hammered).
	if err := term.SendKeys(tuitest.Ctrl('c')); err != nil {
		t.Fatalf("send Ctrl+C: %v", err)
	}

	// Give tuios a beat to process the interrupt, then assert it did not crash.
	// It may still be draining backlog, so we assert liveness rather than a
	// specific frame.
	time.Sleep(300 * time.Millisecond)
	if code, exited := term.ExitCode(); exited {
		t.Fatalf("tuios exited (code %d) after the storm; a clean multiplexer should survive\n%s",
			code, term.Snapshot())
	}

	// Final resize back to a normal size must still succeed on a live process.
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("post-storm resize failed (tuios not alive): %v", err)
	}
}
