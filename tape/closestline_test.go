package tape

import (
	"strings"
	"testing"
)

// TestClosestLinePrefersOverallSimilarity guards against the prefix bias that
// let a four-character line win on the strength of sharing "a ". The line a
// reader needs is the one that is nearly the whole expected string, not the
// shortest thing that happens to start the same way.
func TestClosestLinePrefersOverallSimilarity(t *testing.T) {
	t.Parallel()
	const want = "a headless testing framework for TUIs"
	// The near line differs in its first word, so it shares no prefix at all
	// with want, while "a VT" shares two characters. Prefix scoring picked
	// "a VT"; every other measure says the near line is the one to show.
	const near = "the headless testing framework for TUIs"
	screen := strings.Join([]string{"a VT", "tuitest", near, ""}, "\n")
	got, ok := closestLine(screen, want)
	if !ok {
		t.Fatal("no closest line found")
	}
	if got != near {
		t.Errorf("closestLine = %q, want %q", got, near)
	}
}

// TestClosestLineIgnoresUnrelatedScreens keeps the case where pointing at a
// closest line would be noise: nothing on screen resembles the pattern.
func TestClosestLineIgnoresUnrelatedScreens(t *testing.T) {
	t.Parallel()
	if got, ok := closestLine("\n\n", "hello"); ok {
		t.Errorf("closestLine = %q, true; want no suggestion for a blank screen", got)
	}
}
