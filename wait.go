package tuitest

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Scope selects what part of the screen a match runs against.
type Scope int

const (
	// ScopeScreen matches against the whole rendered screen.
	ScopeScreen Scope = iota
	// ScopeLastLine matches against the last non-blank line only.
	ScopeLastLine
)

// TimeoutError is returned by every wait that times out. Its message includes a
// full screen dump and the tail of the mirrored I/O log, so a failing CI run
// shows exactly what was on screen instead of a bare "timeout".
type TimeoutError struct {
	Op      string
	Want    string
	Elapsed time.Duration
	Screen  string
	TailLog string
}

func (e *TimeoutError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tuitest: %s timed out after %s waiting for %s", e.Op, e.Elapsed.Round(time.Millisecond), e.Want)
	if e.Screen != "" {
		b.WriteString("\n--- screen ---\n")
		b.WriteString(e.Screen)
	}
	if e.TailLog != "" {
		b.WriteString("\n--- last I/O ---\n")
		b.WriteString(e.TailLog)
	}
	return b.String()
}

// ClosedError is returned when the child exits before a wait's condition is met.
type ClosedError struct {
	Op       string
	Want     string
	ExitCode int
	Screen   string
	TailLog  string
}

func (e *ClosedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tuitest: %s: child exited (code %d) before %s", e.Op, e.ExitCode, e.Want)
	if e.Screen != "" {
		b.WriteString("\n--- screen ---\n")
		b.WriteString(e.Screen)
	}
	if e.TailLog != "" {
		b.WriteString("\n--- last I/O ---\n")
		b.WriteString(e.TailLog)
	}
	return b.String()
}

// waitLoop blocks until ready() is true, the child exits, or timeout elapses.
// closedIsOK makes child exit a success rather than an error (used by
// WaitStable). ready is evaluated under the lock on every output wakeup and on a
// short poll fallback for wall-clock conditions. ready builds a screen snapshot
// itself only if it needs one, so cheap conditions such as WaitStable do not pay
// to rebuild the grid on every write during a heavy output burst.
func (t *Terminal) waitLoop(op, want string, timeout time.Duration, closedIsOK bool, ready func() bool) error {
	if timeout <= 0 {
		timeout = time.Second
	}
	start := time.Now()
	deadline := start.Add(timeout)

	t.mu.Lock()
	defer t.mu.Unlock()

	timer := time.AfterFunc(pollInterval, func() {
		t.mu.Lock()
		t.cond.Broadcast()
		t.mu.Unlock()
	})
	defer timer.Stop()

	for {
		if ready() {
			return nil
		}
		if t.exited && !closedIsOK {
			return &ClosedError{
				Op:       op,
				Want:     want,
				ExitCode: t.exitCode,
				Screen:   t.snapshotLocked().Text(),
				TailLog:  t.tailLogLocked(),
			}
		}
		if !time.Now().Before(deadline) {
			return &TimeoutError{
				Op:      op,
				Want:    want,
				Elapsed: time.Since(start),
				Screen:  t.snapshotLocked().Text(),
				TailLog: t.tailLogLocked(),
			}
		}
		timer.Reset(pollInterval)
		t.cond.Wait()
	}
}

func (t *Terminal) tailLogLocked() string {
	return string(t.tailBuf)
}

// WaitFor blocks until cond returns true on the current screen, or timeout.
func (t *Terminal) WaitFor(cond func(Screen) bool, timeout time.Duration) error {
	return t.waitLoop("WaitFor", "custom condition", timeout, false, func() bool {
		return cond(t.snapshotLocked())
	})
}

// WaitForText blocks until the plain-text screen contains substr.
func (t *Terminal) WaitForText(substr string, timeout time.Duration) error {
	return t.waitLoop("WaitForText", fmt.Sprintf("text %q", substr), timeout, false, func() bool {
		return strings.Contains(t.snapshotLocked().Text(), substr)
	})
}

// WaitForMatch blocks until re matches within the given scope.
func (t *Terminal) WaitForMatch(re *regexp.Regexp, scope Scope, timeout time.Duration) error {
	return t.waitLoop("WaitForMatch", fmt.Sprintf("match %s", re.String()), timeout, false, func() bool {
		return re.MatchString(scopeText(t.snapshotLocked(), scope))
	})
}

// WaitStable blocks until no output has arrived for the quiet window (the value
// from WithStabilizeInterval), or timeout. A child that has exited counts as
// stable. Use this after heavy output instead of a fixed Sleep.
func (t *Terminal) WaitStable(timeout time.Duration) error {
	quiet := t.cfg.stabilize
	if quiet <= 0 {
		quiet = DefaultStabilizeInterval
	}
	return t.waitLoop("WaitStable", fmt.Sprintf("output to quiesce for %s", quiet), timeout, true, func() bool {
		if t.exited {
			return true
		}
		return time.Since(t.lastWrite) >= quiet
	})
}

func scopeText(s *screenSnapshot, scope Scope) string {
	switch scope {
	case ScopeLastLine:
		return lastNonBlankLine(s)
	default:
		return s.Text()
	}
}

func lastNonBlankLine(s *screenSnapshot) string {
	for row := s.rows - 1; row >= 0; row-- {
		if line := s.Line(row); line != "" {
			return line
		}
	}
	return ""
}

// --- Semantic (OSC 133) waits ---

// WaitForPrompt blocks until a new shell prompt (OSC 133 A) is drawn. Requires
// WithSemanticMarkers.
func (t *Terminal) WaitForPrompt(timeout time.Duration) error {
	if !t.cfg.semantic {
		return errSemanticDisabled("WaitForPrompt")
	}
	t.mu.Lock()
	base := t.emu.PromptCount()
	t.mu.Unlock()
	return t.waitLoop("WaitForPrompt", "a new shell prompt (OSC 133 A)", timeout, false, func() bool {
		return t.emu.PromptCount() > base
	})
}

// WaitForCommand blocks until the current command finishes (OSC 133 D).
// Requires WithSemanticMarkers.
func (t *Terminal) WaitForCommand(timeout time.Duration) error {
	if !t.cfg.semantic {
		return errSemanticDisabled("WaitForCommand")
	}
	t.mu.Lock()
	base := t.emu.CommandFinishedCount()
	t.mu.Unlock()
	return t.waitLoop("WaitForCommand", "the command to finish (OSC 133 D)", timeout, false, func() bool {
		return t.emu.CommandFinishedCount() > base
	})
}

// LastCommandExit returns the exit code of the last finished command (OSC 133 D)
// and whether one has been seen. Requires WithSemanticMarkers.
func (t *Terminal) LastCommandExit() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.cfg.semantic {
		return 0, false
	}
	return t.emu.LastCommandExit()
}

func errSemanticDisabled(op string) error {
	return fmt.Errorf("tuitest: %s requires WithSemanticMarkers", op)
}
