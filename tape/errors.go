package tape

import (
	"fmt"
	"strings"

	"github.com/Gaurav-Gosain/tuitest"
)

// AssertionFailure marks the error types that mean the program under test did
// not do what the tape said, as opposed to the harness failing to run it. The
// CLI maps every one of them to the assertion exit code, so adding an assertion
// error type cannot silently start reporting a harness failure. The marker
// method is unexported so only this package can claim the meaning.
type AssertionFailure interface {
	error
	assertionFailure()
}

// LineError attaches the tape line number to whatever failed while playing it.
// It is a type rather than a wrapped string so a caller can recover the line
// and the underlying error separately instead of parsing a message.
type LineError struct {
	Line int
	Err  error
}

func (e *LineError) Error() string { return fmt.Sprintf("tape line %d: %v", e.Line, e.Err) }

func (e *LineError) Unwrap() error { return e.Err }

// AssertionError reports a tape assertion that failed: the program under test
// ran, the harness worked, but the screen or exit status was not what the tape
// said it should be. It is a distinct type so the CLI can map a genuine test
// failure to a different exit code than a harness problem, which is the
// difference between "your program is wrong" and "the tool could not run it".
type AssertionError struct {
	Op     string // the tape verb that failed: Expect, Snapshot, or ExpectExit
	Line   int    // the tape line number
	Want   string // what the tape asked for, rendered for a human
	Got    string // what was actually there
	Detail string // an optional rendered difference
}

func (e *AssertionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s failed", e.Op)
	if e.Want != "" {
		fmt.Fprintf(&b, "\n  want: %s", e.Want)
	}
	if e.Got != "" {
		fmt.Fprintf(&b, "\n  got:  %s", e.Got)
	}
	if e.Detail != "" {
		b.WriteString("\n")
		b.WriteString(e.Detail)
	}
	return b.String()
}

func (e *AssertionError) assertionFailure() {}

// SnapshotError reports a Snapshot command whose screen did not match its
// golden file. It keeps the two screens apart rather than pre-rendering a diff
// so that a caller such as tuitest replay can show them side by side, while the
// default Error text stays the unified diff a headless run wants.
type SnapshotError struct {
	Name string
	Path string
	Line int    // the tape line number
	Want string // golden contents
	Got  string // screen at the time of the assertion
}

func (e *SnapshotError) Error() string {
	return fmt.Sprintf("snapshot %q mismatch:\n%s", e.Name, tuitest.Diff(e.Want, e.Got))
}

func (e *SnapshotError) assertionFailure() {}

// ExpectError reports an Expect command whose regex did not match. There is no
// "expected screen" to compare against, only a pattern, so replay renders the
// pattern beside the screen it failed on, while Detail carries the explanation
// a headless run wants: which line came closest and where it first differed.
type ExpectError struct {
	Regex  string
	Scope  tuitest.Scope
	Line   int    // the tape line number
	Screen string // the whole screen at the time of the assertion
	Want   string // what the tape asked for, rendered for a human
	Detail string // an optional rendered explanation of the mismatch
}

func (e *ExpectError) Error() string {
	var b strings.Builder
	b.WriteString("Expect failed")
	if e.Want != "" {
		fmt.Fprintf(&b, "\n  want: %s", e.Want)
	}
	b.WriteString("\n  got:  no match")
	if e.Detail != "" {
		b.WriteString("\n")
		b.WriteString(e.Detail)
	}
	return b.String()
}

func (e *ExpectError) assertionFailure() {}

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
