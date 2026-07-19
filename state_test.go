package tuitest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// buggyOnce builds the buggytui fixture on first use. It is built lazily rather
// than in TestMain so the existing echotui tests do not pay for it.
var (
	buggyOnce sync.Once
	buggyPath string
	// buggyDir is the temp directory holding the built fixture. TestMain
	// removes it after the run; without that the directory was never deleted by
	// anything and every invocation of this package leaked a copy of the
	// binary, which is how a /tmp fills up.
	buggyDir string
	buggyErr error
)

func buggyBinary(t *testing.T) string {
	t.Helper()
	buggyOnce.Do(func() {
		dir, err := os.MkdirTemp("", stateFixturePrefix)
		if err != nil {
			buggyErr = err
			return
		}
		buggyDir = dir
		buggyPath = filepath.Join(dir, "buggytui")
		build := exec.Command("go", "build", "-o", buggyPath, "./testdata/buggytui")
		build.Stderr = os.Stderr
		buggyErr = build.Run()
	})
	if buggyErr != nil {
		t.Fatalf("building buggytui fixture: %v", buggyErr)
	}
	return buggyPath
}

// WaitForOutput exists because WaitStable cannot express "wait for the program
// to react": after a pause the screen is already stable, so WaitStable returns
// without the program having done anything.
//
// Verified to fail on broken code: implementing WaitForOutput as a call to
// WaitStable makes it return immediately, the assertion that the screen changed
// fails, and this test catches it.
func TestWaitForOutputWaitsForAReaction(t *testing.T) {
	t.Parallel()

	term := tuitest.StartT(t, []string{buggyBinary(t), "-bug", "none"}, tuitest.WithSize(80, 10))

	if err := term.WaitForOutput(5 * time.Second); err != nil {
		t.Fatalf("waiting for the first frame: %v", err)
	}
	if err := term.WaitForText("ctrl-c to quit", 5*time.Second); err != nil {
		t.Fatalf("waiting for the banner: %v", err)
	}
	// Let everything go quiet, so WaitStable would now return instantly.
	if err := term.WaitStable(5 * time.Second); err != nil {
		t.Fatalf("waiting for quiet: %v", err)
	}

	before := term.Screen().Text()

	if err := term.SendKeys(tuitest.Down); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForOutput(5 * time.Second); err != nil {
		t.Fatalf("the program should have redrawn after a keystroke: %v", err)
	}

	if after := term.Screen().Text(); after == before {
		t.Fatalf("WaitForOutput returned but the screen never changed; it did not wait for the reaction\nscreen:\n%s", after)
	}
}

// Verified to fail on broken code: making Progress return a constant makes the
// byte counter never advance and this test fails.
func TestProgressCountsOutputBytes(t *testing.T) {
	t.Parallel()

	term := tuitest.StartT(t, []string{buggyBinary(t), "-bug", "none"}, tuitest.WithSize(80, 10))
	if err := term.WaitForText("ctrl-c to quit", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	first, lastWrite := term.Progress()
	if first <= 0 {
		t.Fatalf("Progress reported %d bytes after the program drew a screen", first)
	}
	if lastWrite.IsZero() {
		t.Fatal("Progress reported a zero time for the last write")
	}

	if err := term.SendKeys(tuitest.Up); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForOutput(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	second, _ := term.Progress()
	if second <= first {
		t.Fatalf("byte count did not advance after a redraw: %d then %d", first, second)
	}
}

// A program that restores the terminal must be reported clean, and one that
// does not must be reported dirty. Both directions matter: a check that only
// ever says "dirty" is as useless as one that only ever says "clean".
//
// Verified to fail on broken code: making TermState read the modes but ignore
// AltScreen makes the dirty case report only two of its three problems, and
// making Dirty always return true makes the clean case fail.
func TestTermStateDetectsAnUnrestoredTerminal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		bug       string
		wantDirty bool
	}{
		{"a program that restores the terminal is clean", "none", false},
		{"a program that exits without restoring is dirty", "dirty-exit", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			term := tuitest.StartT(t, []string{buggyBinary(t), "-bug", tc.bug}, tuitest.WithSize(80, 10))
			if err := term.WaitForText("ctrl-c to quit", 5*time.Second); err != nil {
				t.Fatal(err)
			}

			// While running, the program has the alternate screen and mouse
			// tracking on, so it must look dirty either way. That is the
			// baseline proving the modes were actually detected at all.
			if running := term.TermState(); !running.AltScreen || !running.MouseTracking {
				t.Fatalf("while running, the fixture should show alt screen and mouse tracking: %s", running.Describe())
			}

			// Ctrl+C is the fixture's quit key.
			if err := term.SendKeys(tuitest.Ctrl('c')); err != nil {
				t.Fatal(err)
			}
			if _, err := term.Wait(5 * time.Second); err != nil {
				t.Fatalf("the program should have exited: %v", err)
			}
			// Let any final restore sequence be interpreted before judging.
			_ = term.WaitStable(2 * time.Second)

			state := term.TermState()
			if state.Dirty() != tc.wantDirty {
				t.Fatalf("Dirty() = %v, want %v (state: %s)", state.Dirty(), tc.wantDirty, state.Describe())
			}
			if tc.wantDirty {
				for _, want := range []bool{state.AltScreen, state.MouseTracking, state.CursorHidden} {
					if !want {
						t.Errorf("expected alt screen, mouse tracking, and a hidden cursor to all be left set: %s",
							state.Describe())
						break
					}
				}
			}
		})
	}
}

// Verified to fail on broken code: dropping the waitSignal call from ptyproc's
// reap makes Signaled false and this test fails.
func TestExitStatusReportsSignalDeath(t *testing.T) {
	t.Parallel()

	// sleep is a portable program that will sit still until signalled.
	term := tuitest.StartT(t, []string{"sleep", "60"})

	pid := term.Pid()
	if pid <= 0 {
		t.Fatal("Pid returned no process id for a running child")
	}
	// SIGQUIT is not one of the signals Crashed treats as routine teardown, so
	// it exercises the crash path rather than the ignore list.
	if err := exec.Command("kill", "-QUIT", strconv.Itoa(pid)).Run(); err != nil {
		t.Skipf("could not signal the child: %v", err)
	}
	if _, err := term.Wait(10 * time.Second); err != nil {
		t.Fatalf("child did not exit after SIGQUIT: %v", err)
	}

	st, exited := term.ExitStatus()
	if !exited {
		t.Fatal("ExitStatus reports the child has not exited")
	}
	if !st.Signaled {
		t.Fatalf("ExitStatus should report signal death, got %+v", st)
	}
	if !st.Crashed() {
		t.Fatalf("a child killed by SIGQUIT should count as crashed, got %+v", st)
	}
}

// Verified to fail on broken code: making Paste write the text without the
// bracketed-paste markers makes the assertion on the start marker fail.
func TestPasteWrapsTextInBracketedPasteMarkers(t *testing.T) {
	t.Parallel()

	term := tuitest.StartT(t, []string{buggyBinary(t), "-bug", "none"}, tuitest.WithSize(80, 12))
	if err := term.WaitForText("ctrl-c to quit", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	if err := term.Paste("hi"); err != nil {
		t.Fatal(err)
	}
	// The fixture echoes what it read as a printable summary, so the paste
	// markers show up as the escape sequences that delimit them.
	if err := term.WaitForText("<ESC>[200~hi<ESC>[201~", 5*time.Second); err != nil {
		t.Fatalf("the pasted text should arrive wrapped in bracketed-paste markers: %v\nscreen:\n%s",
			err, term.Screen().Text())
	}
}
