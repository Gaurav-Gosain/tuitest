package tape

import (
	"fmt"
	"strings"
)

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
