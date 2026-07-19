package tape

import (
	"fmt"
	"strings"

	"github.com/Gaurav-Gosain/tuitest"
)

// SnapshotError reports a Snapshot command whose screen did not match its
// golden file. It keeps the two screens apart rather than pre-rendering a diff
// so that a caller such as tuitest replay can show them side by side, while the
// default Error text stays the unified diff a headless run wants.
type SnapshotError struct {
	Name string
	Path string
	Want string // golden contents
	Got  string // screen at the time of the assertion
}

func (e *SnapshotError) Error() string {
	return fmt.Sprintf("snapshot %q mismatch:\n%s", e.Name, tuitest.Diff(e.Want, e.Got))
}

// ExpectError reports an Expect command whose regex did not match. There is no
// "expected screen" to compare against, only a pattern, so replay renders the
// pattern beside the screen it failed on.
type ExpectError struct {
	Regex  string
	Scope  tuitest.Scope
	Screen string
}

func (e *ExpectError) Error() string {
	return fmt.Sprintf("Expect %s did not match; screen was:\n%s", e.Regex, e.Screen)
}

// SideBySide renders want and got in two labelled columns, marking rows that
// differ with a '|' gutter. It is what replay prints when an assertion fails,
// on the theory that an operator watching a TUI wants to see the frame, not
// reconstruct it from a line diff.
func SideBySide(wantLabel, want, gotLabel, got string, width int) string {
	if width <= 0 {
		width = 40
	}
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := max(len(wl), len(gl))

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s   %s\n", width, wantLabel, gotLabel)
	fmt.Fprintf(&b, "%s   %s\n", strings.Repeat("-", width), strings.Repeat("-", width))
	for i := range n {
		var l, r string
		if i < len(wl) {
			l = wl[i]
		}
		if i < len(gl) {
			r = gl[i]
		}
		gutter := " "
		if l != r {
			gutter = "|"
		}
		fmt.Fprintf(&b, "%-*s %s %s\n", width, truncate(l, width), gutter, truncate(r, width))
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncate clips s to width columns, counting runes rather than bytes so that
// non-ASCII screens do not blow the column alignment.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "."
	}
	return string(r[:width-1]) + "."
}
