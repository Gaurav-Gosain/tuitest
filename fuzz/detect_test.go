package fuzz

import (
	"strings"
	"syscall"
	"testing"

	"github.com/Gaurav-Gosain/tuitest"
)

// Signal aliases, so the table below reads as intent rather than as syscall
// constants inline.
const (
	syscallSIGSEGV = syscall.SIGSEGV
	syscallSIGABRT = syscall.SIGABRT
	syscallSIGTERM = syscall.SIGTERM
	syscallSIGINT  = syscall.SIGINT
)

// fakeScreen implements tuitest.Screen so the screen-model checks can be
// exercised on states a real emulator will not readily produce.
type fakeScreen struct {
	cols, rows     int
	curCol, curRow int
	visible        bool
	text           string
	exitCode       int
	exited         bool
}

func (s fakeScreen) Size() (int, int)           { return s.cols, s.rows }
func (s fakeScreen) Cell(int, int) tuitest.Cell { return tuitest.Cell{Rune: ' ', Width: 1} }
func (s fakeScreen) Cursor() (int, int, bool)   { return s.curCol, s.curRow, s.visible }
func (s fakeScreen) Text() string               { return s.text }
func (s fakeScreen) Line(int) string            { return s.text }
func (s fakeScreen) ExitCode() (int, bool)      { return s.exitCode, s.exited }

func TestCheckScreenModelAcceptsAConsistentScreen(t *testing.T) {
	t.Parallel()
	sc := fakeScreen{cols: 80, rows: 24, curCol: 79, curRow: 23, visible: true}
	if f := checkScreenModel(sc, 80, 24); f != nil {
		t.Fatalf("a cursor at the last valid cell must not be a failure, got %s: %s", f.Kind, f.Detail)
	}
}

// Verified to fail on broken code: replacing the cursor bounds test with
// `col > cols || row > rows` (an off-by-one that permits the cursor one cell
// past the edge) makes this case return nil and the test fails.
func TestCheckScreenModelRejectsCursorOutOfBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		curCol, curRow int
	}{
		{"one column past the right edge", 80, 0},
		{"one row past the bottom", 0, 24},
		{"far outside", 500, 500},
		{"negative column", -1, 0},
		{"negative row", 0, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := fakeScreen{cols: 80, rows: 24, curCol: tc.curCol, curRow: tc.curRow, visible: true}
			f := checkScreenModel(sc, 80, 24)
			if f == nil {
				t.Fatalf("cursor at (%d,%d) in an 80x24 grid must be reported", tc.curCol, tc.curRow)
			}
			if f.Kind != FailScreenInconsistent {
				t.Fatalf("kind = %s, want %s", f.Kind, FailScreenInconsistent)
			}
			if !strings.Contains(f.Detail, "outside") {
				t.Fatalf("detail should say the cursor is outside the grid, got %q", f.Detail)
			}
		})
	}
}

// Verified to fail on broken code: dropping the size comparison entirely makes
// this return nil and the test fails.
func TestCheckScreenModelRejectsUnrequestedResize(t *testing.T) {
	t.Parallel()
	sc := fakeScreen{cols: 40, rows: 12, curCol: 0, curRow: 0, visible: true}
	f := checkScreenModel(sc, 80, 24)
	if f == nil {
		t.Fatal("a 40x12 grid when 80x24 was requested must be reported")
	}
	if f.Kind != FailScreenInconsistent {
		t.Fatalf("kind = %s, want %s", f.Kind, FailScreenInconsistent)
	}
}

// Verified to fail on broken code: making Crashed report true for a zero exit
// makes the "clean exit" case fail, and making it ignore Signaled makes the
// SIGSEGV case fail.
func TestExitStatusCrashedDistinguishesRealCrashes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		st   tuitest.ExitStatus
		want bool
	}{
		{"clean exit is not a crash", tuitest.ExitStatus{Code: 0}, false},
		{"non-zero exit is a crash", tuitest.ExitStatus{Code: 1}, true},
		{"go panic exit code is a crash", tuitest.ExitStatus{Code: 2}, true},
		{
			"segfault is a crash",
			tuitest.ExitStatus{Code: -1, Signaled: true, Signal: syscallSIGSEGV},
			true,
		},
		{
			"abort is a crash",
			tuitest.ExitStatus{Code: -1, Signaled: true, Signal: syscallSIGABRT},
			true,
		},
		{
			// The harness itself sends SIGTERM to tear a program down, so
			// treating it as a crash would make every torn-down iteration a
			// false positive.
			"SIGTERM from our own teardown is not a crash",
			tuitest.ExitStatus{Code: -1, Signaled: true, Signal: syscallSIGTERM},
			false,
		},
		{
			"SIGINT from Ctrl+C is not a crash",
			tuitest.ExitStatus{Code: -1, Signaled: true, Signal: syscallSIGINT},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.st.Crashed(); got != tc.want {
				t.Fatalf("Crashed() = %v, want %v for %+v", got, tc.want, tc.st)
			}
		})
	}
}
