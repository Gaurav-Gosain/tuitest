package tuitest

import (
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Private mode numbers that matter when judging whether a program cleaned up
// after itself. These are the DEC private modes a full-screen TUI turns on at
// startup and is expected to turn off before exiting.
const (
	modeAltScreen           = 1047
	modeAltScreenSaveCursor = 1049
	modeMouseX10            = 9
	modeMouseNormal         = 1000
	modeMouseHighlight      = 1001
	modeMouseButtonEvent    = 1002
	modeMouseAnyEvent       = 1003
	modeFocusEvent          = 1004
	modeBracketedPaste      = 2004
)

// TermState is a snapshot of the terminal modes a program has left set, plus
// cursor visibility. It answers "did this TUI restore the terminal?", which is
// a common and user-visible bug class: a program that exits without leaving the
// alternate screen, or with mouse reporting still on, leaves the user's shell
// unusable.
type TermState struct {
	AltScreen      bool
	MouseTracking  bool
	BracketedPaste bool
	FocusReporting bool
	CursorHidden   bool
	rawModes       map[int]bool
}

// Dirty reports whether any mode is left in a state that would visibly damage
// the user's shell after the program exits.
func (s TermState) Dirty() bool {
	return s.AltScreen || s.MouseTracking || s.BracketedPaste || s.FocusReporting || s.CursorHidden
}

// Describe lists the offending modes in a stable order, for error messages.
func (s TermState) Describe() string {
	var parts []string
	if s.AltScreen {
		parts = append(parts, "alternate screen still active")
	}
	if s.MouseTracking {
		parts = append(parts, "mouse tracking still enabled")
	}
	if s.BracketedPaste {
		parts = append(parts, "bracketed paste still enabled")
	}
	if s.FocusReporting {
		parts = append(parts, "focus reporting still enabled")
	}
	if s.CursorHidden {
		parts = append(parts, "cursor still hidden")
	}
	if len(parts) == 0 {
		return "clean"
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// TermState returns the current mode state of the emulated terminal. Call it
// after the child has exited to check that it restored the terminal.
func (t *Terminal) TermState() TermState {
	t.mu.Lock()
	defer t.mu.Unlock()
	modes := t.emu.Modes()
	_, _, visible := t.emu.Cursor()
	return TermState{
		AltScreen:      modes[modeAltScreen] || modes[modeAltScreenSaveCursor],
		MouseTracking:  modes[modeMouseX10] || modes[modeMouseNormal] || modes[modeMouseHighlight] || modes[modeMouseButtonEvent] || modes[modeMouseAnyEvent],
		BracketedPaste: modes[modeBracketedPaste],
		FocusReporting: modes[modeFocusEvent],
		CursorHidden:   !visible,
		rawModes:       modes,
	}
}

// Mode reports whether the given DEC private mode number is currently set.
func (s TermState) Mode(n int) bool { return s.rawModes[n] }

// ExitStatus describes how the child finished.
type ExitStatus struct {
	// Code is the exit status, or -1 when the child died from a signal.
	Code int
	// Signaled is true when the child was killed by a signal rather than
	// exiting on its own.
	Signaled bool
	// Signal is the killing signal when Signaled is true.
	Signal syscall.Signal
}

// Crashed reports whether the child died in a way that indicates a bug: killed
// by a fault signal, or exited non-zero. A clean zero exit is not a crash.
func (s ExitStatus) Crashed() bool {
	if s.Signaled {
		switch s.Signal {
		case syscall.SIGTERM, syscall.SIGKILL, syscall.SIGHUP, syscall.SIGINT, syscall.SIGPIPE:
			// Signals we or the harness teardown send ourselves, or that a
			// terminal hangup delivers. Not evidence of a bug.
			return false
		default:
			return true
		}
	}
	return s.Code > 0
}

func (s ExitStatus) String() string {
	if s.Signaled {
		return "killed by " + s.Signal.String()
	}
	return "exit status " + strconv.Itoa(s.Code)
}

// ExitStatus reports how the child finished and whether it has exited at all.
func (t *Terminal) ExitStatus() (ExitStatus, bool) {
	if t.proc == nil {
		return ExitStatus{Code: -1}, false
	}
	st, exited := t.proc.ExitStatus()
	return ExitStatus{Code: st.Code, Signaled: st.Signaled, Signal: st.Signal}, exited
}

// Pid returns the child's process id, or 0 if it is not running.
func (t *Terminal) Pid() int {
	if t.proc == nil {
		return 0
	}
	return t.proc.Pid()
}
