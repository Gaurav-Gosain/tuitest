package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// render turns an error into the text the CLI prints.
//
// The library prefixes its own errors with "tuitest: " so they stand out in Go
// test output, but on the command line the program name is already printed, so
// it is trimmed rather than shown twice. Only a leading prefix is trimmed, so a
// screen dump that happens to contain the word is left alone.
func render(err error) string {
	var le *tape.LineError
	if errors.As(err, &le) {
		return fmt.Sprintf("tape line %d: %s", le.Line, trimToolPrefix(le.Err.Error()))
	}
	return trimToolPrefix(err.Error())
}

func trimToolPrefix(s string) string { return strings.TrimPrefix(s, "tuitest: ") }

// classify maps an error from the parser, the harness, or the tape player onto
// one of the tool's exit codes.
//
// The interesting judgement is ClosedError, which means the program under test
// exited before a wait was satisfied. That is counted as an assertion failure,
// not a harness error: the harness did its job, the program simply did not do
// what the tape said it would.
func classify(err error) int {
	if err == nil {
		return ExitOK
	}
	var pe *tape.ParseError
	if errors.As(err, &pe) {
		return ExitUsage
	}
	var te *tuitest.TimeoutError
	if errors.As(err, &te) {
		return ExitTimeout
	}
	// Every tape assertion type, not just AssertionError: Snapshot and Expect
	// failures report through their own types so replay can render them, and
	// they mean exactly the same thing to a CI script.
	var af tape.AssertionFailure
	if errors.As(err, &af) {
		return ExitAssert
	}
	var ce *tuitest.ClosedError
	if errors.As(err, &ce) {
		return ExitAssert
	}
	return ExitHarness
}

// kindOf names an exit code for machine-readable output.
func kindOf(code int) string {
	switch code {
	case ExitOK:
		return "ok"
	case ExitAssert:
		return "assertion"
	case ExitUsage:
		return "usage"
	case ExitTimeout:
		return "timeout"
	default:
		return "harness"
	}
}
