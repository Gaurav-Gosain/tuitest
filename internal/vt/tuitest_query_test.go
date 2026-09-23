package vt

import (
	"strings"
	"testing"
)

// ask writes a query and returns whatever the emulator wants to send back.
func ask(t *testing.T, e *Emulator, query string) string {
	t.Helper()
	if _, err := e.WriteString(query); err != nil {
		t.Fatalf("writing %q: %v", query, err)
	}
	return string(e.TakeResponses())
}

// TestCursorPositionReportIsLineThenColumn pins the order of a CPR reply.
//
// A reply with the two numbers the wrong way round is worse than no reply,
// because a program cannot tell it apart from a correct one: it reads a column
// as a line and works out a width or a prompt position from it. On a square
// screen the two are indistinguishable, so this asks from a cell whose row and
// column differ and whose column is past the last row.
func TestCursorPositionReportIsLineThenColumn(t *testing.T) {
	t.Parallel()

	e := NewEmulator(80, 10)
	// CUP is line then column too, so this puts the cursor on line 3, column 40.
	if _, err := e.WriteString("\x1b[3;40H"); err != nil {
		t.Fatalf("positioning the cursor: %v", err)
	}
	e.TakeResponses()

	if got, want := ask(t, e, "\x1b[6n"), "\x1b[3;40R"; got != want {
		t.Errorf("CPR replied %q, want %q", got, want)
	}
	if got, want := ask(t, e, "\x1b[?6n"), "\x1b[?3;40R"; got != want {
		t.Errorf("DECXCPR replied %q, want %q", got, want)
	}
}

// TestCursorPositionReportIsRelativeUnderOriginMode covers the other half of
// the contract: under DECOM a program addresses the screen relative to the
// scroll region, so a report in absolute coordinates contradicts the addressing
// the same program is using.
func TestCursorPositionReportIsRelativeUnderOriginMode(t *testing.T) {
	t.Parallel()

	e := NewEmulator(80, 24)
	// A region starting at line 5, origin mode on, then home.
	if _, err := e.WriteString("\x1b[5;20r\x1b[?6h\x1b[1;1H"); err != nil {
		t.Fatalf("setting up origin mode: %v", err)
	}
	e.TakeResponses()

	if got, want := ask(t, e, "\x1b[6n"), "\x1b[1;1R"; got != want {
		t.Errorf("CPR under origin mode replied %q, want %q", got, want)
	}
}

// TestRequestedModesAreRecognized is the check that stops the emulator
// disowning a feature it implements.
//
// DECRQM answers out of the mode table, and a mode missing from it is reported
// as "not recognized" rather than as off. A program that probes before enabling
// then takes its fallback path forever, and the whole suite runs against a
// configuration nobody would ever ship. That is how synchronized output stayed
// off here for as long as it did, so the modes this emulator acts on are pinned
// rather than left to whoever edits the table next.
func TestRequestedModesAreRecognized(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"synchronized output", "\x1b[?2026$p", "\x1b[?2026;2$y"},
		{"in-band resize", "\x1b[?2048$p", "\x1b[?2048;2$y"},
		{"bracketed paste", "\x1b[?2004$p", "\x1b[?2004;2$y"},
		{"focus events", "\x1b[?1004$p", "\x1b[?1004;2$y"},
		{"unicode core", "\x1b[?2027$p", "\x1b[?2027;2$y"},
		{"insert replace", "\x1b[4$p", "\x1b[4;2$y"},
		{"sgr pixel mouse", "\x1b[?1016$p", "\x1b[?1016;2$y"},
		{"original alternate screen", "\x1b[?47$p", "\x1b[?47;2$y"},
		{"autowrap, which is on by default", "\x1b[?7$p", "\x1b[?7;1$y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := NewEmulator(80, 24)
			if got := ask(t, e, tc.query); got != tc.want {
				t.Errorf("%s: replied %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// TestUnknownModeIsStillAnswered is the other side of it: a mode nothing
// implements has to come back as unrecognized rather than as silence, or a
// program that waits for the answer waits for its whole timeout.
func TestUnknownModeIsStillAnswered(t *testing.T) {
	t.Parallel()

	e := NewEmulator(80, 24)
	got := ask(t, e, "\x1b[?7777$p")
	if !strings.HasPrefix(got, "\x1b[?7777;") {
		t.Fatalf("a mode nobody defines went unanswered, got %q", got)
	}
	if got != "\x1b[?7777;0$y" {
		t.Errorf("replied %q, want the not-recognized answer %q", got, "\x1b[?7777;0$y")
	}
}
