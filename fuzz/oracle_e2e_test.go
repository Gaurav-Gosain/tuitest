package fuzz_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/fuzz"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// markerText is the character testdata/buggytui draws at the top left of every
// frame, and which the lose-marker bug drops permanently once F9 is pressed.
const markerText = "*"

// markerInvariant is the kind of property a caller actually writes, escape
// hatches and all. Both guards are load-bearing, and both were added because
// the control below reported the fixture rather than the detectors.
//
// The exit guard, because the fuzzer sends Ctrl+c freely and a program that has
// quit has no screen to hold up.
//
// The size guard, because at one column by one row the fixture writes four
// single-character lines, each newline scrolls the one before it away, and the
// screen ends up blank. No program maintains a layout property at that size, so
// asserting one is a bug in the invariant rather than in the program. The cost
// is that the property goes vacuous on exactly the degenerate sizes the fuzzer
// favours, and that trade is the main limit of the feature; see
// docs/fuzzing.md.
func markerInvariant() fuzz.Invariant {
	return fuzz.Invariant{
		Name: "marker is present",
		Check: func(sc tuitest.Screen) error {
			if _, exited := sc.ExitCode(); exited {
				return nil
			}
			if cols, rows := sc.Size(); cols < 20 || rows < 5 {
				return nil
			}
			if strings.HasPrefix(sc.Line(0), markerText) {
				return nil
			}
			return errors.New("the marker " + strconv.Quote(markerText) + " is not at the top left")
		},
	}
}

// Verified to fail on broken code: making monitor.observeInvariants a no-op, or
// removing the Invariants field from the monitor's construction, makes the
// fuzzer miss the violation and this test fails.
func TestFindsViolatedInvariantAndMinimisesTowardIt(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "lose-marker")
	opts.Seed = 11
	// The latch is bound to one key out of a large space, so this needs enough
	// iterations to be sure of sending it.
	opts.Iterations = 40
	opts.Invariants = []fuzz.Invariant{markerInvariant()}
	// Resizes are off so the property is never vacuous. The invariant goes
	// quiet at a degenerate size, and a run that ends at one column would be
	// judged on a screen no property holds on. What is under test here is
	// discovery and minimisation, not the guard.
	opts.Gen.NoResize = true

	res := runFuzz(t, opts)

	f := findFailure(res, fuzz.FailInvariant)
	if f == nil {
		t.Fatalf("expected an invariant finding, got:\n%s", summarise(res))
	}
	if f.Invariant != "marker is present" {
		t.Fatalf("Invariant = %q, want the name the caller gave it", f.Invariant)
	}

	// The shrinker treats this as an ordinary failure, so the reproduction must
	// be minimal and must still contain the key that latches the marker off.
	// That combination, a user-supplied oracle plus real minimisation, is the
	// whole point of the feature: bombadil, where the idea comes from, can find
	// a violation like this but has no way to reduce the run that found it.
	trigger := indexOfKey(f.Commands, "F9")
	if trigger < 0 {
		t.Fatalf("the reproduction must keep the key that loses the marker:\n%s",
			tape.Sprint(f.Commands))
	}
	if len(f.Commands) > 8 {
		t.Fatalf("reproduction is %d commands, expected minimisation:\n%s",
			len(f.Commands), tape.Sprint(f.Commands))
	}
	if f.Original <= len(f.Commands) {
		t.Errorf("Original=%d and minimised=%d: minimisation should have reduced the input",
			f.Original, len(f.Commands))
	}
	// Confirmation is a real replay of a real program, so asserting it is only
	// fair where the reproduction is small enough not to race the program's
	// redraw. This one minimises to a Spawn and a single key, which it does
	// reliably; the larger reproductions two other tests produce do not, and
	// docs/limits.md says so.
	if !f.Verified {
		t.Error("the minimised reproduction did not reproduce on confirmation")
	}

	// The onset must point at a real command in the minimised tape, and land on
	// the triggering key rather than at the end of the run. It may lag the key by
	// a command or two, because the violation only becomes observable once the
	// program has redrawn. What it must not be is the command that happened to be
	// running when the checker looked, which would send the reader to the wrong
	// line of the file they were just handed.
	if f.Onset <= 0 || f.Onset > len(f.Commands) {
		t.Fatalf("Onset = %d is not an index into the %d-command reproduction", f.Onset, len(f.Commands))
	}
	if f.Onset < trigger || f.Onset > trigger+3 {
		t.Errorf("Onset = %d should land on or just after the F9 at index %d, in:\n%s",
			f.Onset, trigger, tape.Sprint(f.Commands))
	}
	if !strings.Contains(f.Detail, "first failed after command") {
		t.Errorf("the detail should report the onset, got %q", f.Detail)
	}
}

// Verified to fail on broken code: making checkReplacementChars return nil
// unconditionally, or dropping the checkReplacement call from monitor.check,
// makes the fuzzer miss the mangled text and this test fails.
func TestFindsReplacementCharacterFromWellFormedInput(t *testing.T) {
	t.Parallel()

	opts := baseOptions(t, "mangle-unicode")
	opts.Seed = 4
	opts.Iterations = 20
	opts.Limits.DetectReplacementChars = true
	// The check is only sound on well-formed input, and hostile bursts are
	// malformed by design, so the two go together. See docs/fuzzing.md.
	opts.Gen.NoHostile = true

	res := runFuzz(t, opts)

	f := findFailure(res, fuzz.FailReplacementChar)
	if f == nil {
		t.Fatalf("expected a replacement-char finding, got:\n%s", summarise(res))
	}
	if !strings.Contains(f.Detail, "U+FFFD") {
		t.Errorf("the detail should name the character, got %q", f.Detail)
	}
	if !strings.ContainsRune(f.Screen, '�') {
		t.Errorf("the captured screen should contain the character that was reported:\n%s", f.Screen)
	}
	// Everything in the reproduction must be well-formed, or the finding would
	// be the fuzzer reporting the program for correctly echoing bad bytes.
	for _, c := range f.Commands {
		if c.Text != "" && !isWellFormed(c.Text) {
			t.Fatalf("the reproduction sends malformed input %q, so the finding is not sound:\n%s",
				c.Text, tape.Sprint(f.Commands))
		}
	}
}

// The control for both features together. A well-behaved program checked with a
// reasonable invariant and the replacement-character detector on must produce
// nothing at all, across several seeds. This is the test that decides whether
// the features are usable: a fuzzer that cries wolf trains you to ignore it.
func TestOraclesStaySilentOnAWellBehavedProgram(t *testing.T) {
	t.Parallel()

	for _, seed := range []uint64{1, 2, 3, 7, 11, 13} {
		t.Run("seed"+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			opts := baseOptions(t, "none")
			opts.Seed = seed
			opts.Iterations = 8
			opts.StopOnFirst = false
			opts.Invariants = []fuzz.Invariant{markerInvariant()}
			opts.Limits.DetectReplacementChars = true

			res := runFuzz(t, opts)
			if len(res.Failures) != 0 {
				t.Fatalf("the well-behaved fixture must produce no findings, got:\n%s", summarise(res))
			}
		})
	}
}

// indexOfKey returns the one-based index of the first command sending the named
// key, or -1.
func indexOfKey(cmds []tape.Command, key string) int {
	for i, c := range cmds {
		if c.Kind != tape.KindKey {
			continue
		}
		for _, k := range c.Keys {
			if k == key {
				return i + 1
			}
		}
	}
	return -1
}

// isWellFormed mirrors the gate monitor.noteCommand applies, so the test checks
// the property the detector claims rather than restating its implementation.
func isWellFormed(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, utf8.RuneError)
}
