package fuzz

import (
	"errors"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// Verified to fail on broken code: making checkReplacementChars return nil
// unconditionally makes every positive case here fail.
func TestCheckReplacementCharsFindsTheCharacterAndSaysWhere(t *testing.T) {
	t.Parallel()

	sc := fakeScreen{
		cols: 20, rows: 3,
		lines: []string{"all fine here", "caf� au lait", ""},
		text:  "all fine here\ncaf� au lait",
	}
	f := checkReplacementChars(sc)
	if f == nil {
		t.Fatal("a replacement character on screen must be reported")
	}
	if f.Kind != FailReplacementChar {
		t.Fatalf("kind = %s, want %s", f.Kind, FailReplacementChar)
	}
	// The column is counted in runes, not bytes, because a byte offset into a
	// line containing multi-byte text does not name a cell anyone can find.
	if !strings.Contains(f.Detail, "row 1") || !strings.Contains(f.Detail, "column 3") {
		t.Fatalf("detail must locate the character, got %q", f.Detail)
	}
}

func TestCheckReplacementCharsAcceptsACleanScreen(t *testing.T) {
	t.Parallel()

	sc := fakeScreen{cols: 20, rows: 3, lines: []string{"café", "你好", ""}}
	if f := checkReplacementChars(sc); f != nil {
		t.Fatalf("well-formed text must not be reported, got %s: %s", f.Kind, f.Detail)
	}
}

// The gate that keeps the check honest. Once the fuzzer has sent bytes that
// cannot be decoded, drawing a replacement character is the correct response
// and reporting it would be a false positive on a program doing the right
// thing.
//
// Verified to fail on broken code: dropping the payload test from
// monitor.noteCommand leaves the check live after malformed input and all three
// poisoning cases fail.
func TestReplacementCheckIsGatedOnWellFormedInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		sent tape.Command
		want bool
	}{
		{"well-formed text keeps the check live", tape.Command{Kind: tape.KindRaw, Text: "hello"}, true},
		{"a truncated rune poisons the run", tape.Command{Kind: tape.KindRaw, Text: "caf\xc3"}, false},
		{"a hostile burst poisons the run", tape.Command{Kind: tape.KindRaw, Text: "\x80\x80"}, false},
		{"a literal U+FFFD poisons the run", tape.Command{Kind: tape.KindRaw, Text: "�"}, false},
		{"a key event leaves it live", tape.Command{Kind: tape.KindKey, Keys: []string{"Up"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &monitor{
				limits:          Limits{DetectReplacementChars: true},
				inputWellFormed: true,
			}
			m.noteCommand(tc.sent)
			if m.inputWellFormed != tc.want {
				t.Fatalf("inputWellFormed = %v after %q, want %v", m.inputWellFormed, tc.sent.Text, tc.want)
			}
		})
	}
}

// newInvariantMonitor builds a monitor whose screen is the string the caller
// controls, so a scripted sequence of frames stands in for a program's redraws.
func newInvariantMonitor(t *testing.T, inv []Invariant, frames *string) *monitor {
	t.Helper()
	m := &monitor{invariants: inv, invState: make([]invariantState, len(inv)), drawn: true}
	m.screenFn = func() tuitest.Screen {
		return fakeScreen{cols: 20, rows: 3, text: *frames}
	}
	return m
}

// The settle gate. An invariant that fails between two ordinary commands and
// recovers before the next wait is a redraw in progress, not a bug, and
// reporting it would make the feature worse than useless.
//
// Verified to fail on broken code: making reportInvariants ignore the streak
// and report on st.failing alone makes the transient case report.
func TestInvariantTransientBetweenCommandsIsNotReported(t *testing.T) {
	t.Parallel()

	var frame string
	inv := []Invariant{{
		Name: "footer",
		Check: func(sc tuitest.Screen) error {
			if strings.Contains(sc.Text(), "footer") {
				return nil
			}
			return errors.New("footer missing")
		},
	}}
	m := newInvariantMonitor(t, inv, &frame)

	// Command 1: a good frame.
	frame = "body\nfooter"
	m.cmdIndex++
	m.observeInvariants()

	// Command 2: the screen is caught mid-redraw with the footer not yet
	// painted. Nothing is reported, because this is not a wait.
	frame = "body"
	m.cmdIndex++
	m.observeInvariants()
	if f := m.reportInvariants(false); f != nil {
		t.Fatalf("a mid-render frame must not be reported, got %s", f.Detail)
	}

	// Command 3 is a wait, and the redraw has landed by the time it returns.
	frame = "body\nfooter"
	m.cmdIndex++
	m.observeInvariants()
	if f := m.reportInvariants(false); f != nil {
		t.Fatalf("a recovered invariant must not be reported, got %s", f.Detail)
	}
}

// Verified to fail on broken code: recording the onset at report time rather
// than at the first failing observation makes the onset assertion fail.
func TestInvariantReportsTheOnsetNotTheNoticeTime(t *testing.T) {
	t.Parallel()

	var frame string
	inv := []Invariant{{
		Name: "footer",
		Check: func(sc tuitest.Screen) error {
			if strings.Contains(sc.Text(), "footer") {
				return nil
			}
			return errors.New("footer missing")
		},
	}}
	m := newInvariantMonitor(t, inv, &frame)

	frame = "body\nfooter"
	for i := 0; i < 3; i++ {
		m.cmdIndex++
		m.observeInvariants()
	}
	// Command 4 breaks it, and it stays broken through commands 5 and 6.
	frame = "body"
	for i := 0; i < 3; i++ {
		m.cmdIndex++
		m.observeInvariants()
	}

	f := m.reportInvariants(false)
	if f == nil {
		t.Fatal("a standing violation must be reported")
	}
	if f.Kind != FailInvariant {
		t.Fatalf("kind = %s, want %s", f.Kind, FailInvariant)
	}
	if f.Invariant != "footer" {
		t.Fatalf("Invariant = %q, want %q", f.Invariant, "footer")
	}
	if f.Onset != 4 {
		t.Fatalf("Onset = %d, want 4: the report must point at where the property broke, not where it was noticed at %d", f.Onset, m.cmdIndex)
	}
}

// A streak that begins at the very command doing the reporting has not yet
// survived a settle, so it waits for the next one.
func TestInvariantFailingAtTheReportingCommandWaitsForTheNextSettle(t *testing.T) {
	t.Parallel()

	var frame string
	inv := []Invariant{{
		Name: "footer",
		Check: func(sc tuitest.Screen) error {
			if strings.Contains(sc.Text(), "footer") {
				return nil
			}
			return errors.New("footer missing")
		},
	}}
	m := newInvariantMonitor(t, inv, &frame)

	frame = "body"
	m.cmdIndex++
	m.observeInvariants()
	if f := m.reportInvariants(false); f != nil {
		t.Fatalf("a violation first seen at this command must not report yet, got %s", f.Detail)
	}
	m.cmdIndex++
	m.observeInvariants()
	if f := m.reportInvariants(false); f == nil {
		t.Fatal("a violation that survived into the next command must report")
	}
}

// Two invariants must stay two findings: the shrinker's acceptance test and the
// session's deduplication both key on the name, so a session cannot silently
// minimise toward a property the user was not looking at.
//
// Verified to fail on broken code: dropping the Invariant comparison from
// sameFailure makes the mismatched pair compare equal.
func TestSameFailureKeepsDistinctInvariantsApart(t *testing.T) {
	t.Parallel()

	a := &Failure{Kind: FailInvariant, Invariant: "footer"}
	b := &Failure{Kind: FailInvariant, Invariant: "status bar"}
	if sameFailure(a, b) {
		t.Fatal("two different invariants must not count as the same failure")
	}
	if !sameFailure(a, &Failure{Kind: FailInvariant, Invariant: "footer"}) {
		t.Fatal("the same invariant must count as the same failure")
	}
	if sameFailure(a, &Failure{Kind: FailCrash}) {
		t.Fatal("different kinds must not count as the same failure")
	}
	if sameFailure(nil, a) || sameFailure(a, nil) {
		t.Fatal("a missing observation is never the same failure")
	}
	// Kinds without a name are still compared on kind alone.
	if !sameFailure(&Failure{Kind: FailCrash}, &Failure{Kind: FailCrash, Detail: "different words"}) {
		t.Fatal("a crash must match a crash regardless of the detail text")
	}
}

func TestFailureKeySeparatesInvariantsAndNotOtherKinds(t *testing.T) {
	t.Parallel()

	if failureKey(&Failure{Kind: FailInvariant, Invariant: "a"}) ==
		failureKey(&Failure{Kind: FailInvariant, Invariant: "b"}) {
		t.Fatal("two invariants must not share a deduplication key")
	}
	if failureKey(&Failure{Kind: FailCrash, Invariant: "ignored"}) != string(FailCrash) {
		t.Fatal("a non-invariant kind must key on the kind alone")
	}
}
