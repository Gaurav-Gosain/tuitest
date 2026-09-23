package tuitest

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors for the three ways a wait can fail. Every wait returns an
// error that wraps one of these, so a caller can branch on the kind of failure
// without type-asserting the concrete error:
//
//	if err := term.WaitForText("ready", time.Second); errors.Is(err, tuitest.ErrTimeout) {
//		// the program is merely slow
//	}
var (
	// ErrTimeout is wrapped by every wait that runs out of time.
	ErrTimeout = errors.New("tuitest: timed out")
	// ErrChildExited is wrapped when the program under test exits before a
	// wait's condition is met, and by input (Type, SendKeys, Paste, SendMouse,
	// Resize) that failed because the program has exited.
	ErrChildExited = errors.New("tuitest: child exited before the condition was met")
	// ErrSemanticMarkers is wrapped by the OSC 133 waits when the terminal was
	// started without WithSemanticMarkers.
	ErrSemanticMarkers = errors.New("tuitest: semantic markers are not enabled (use WithSemanticMarkers)")
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
// shows exactly what was on screen instead of a bare "timeout". It unwraps to
// ErrTimeout.
type TimeoutError struct {
	// Op is the wait that failed, such as "WaitForText".
	Op string
	// Want describes the condition in words, such as `text "ready"`.
	Want string
	// Elapsed is how long the wait actually took.
	Elapsed time.Duration
	// Screen is the plain-text screen at the moment of the failure.
	Screen string
	// TailLog is the tail of the mirrored PTY I/O.
	TailLog string
}

// Unwrap makes errors.Is(err, ErrTimeout) true for every wait timeout.
func (e *TimeoutError) Unwrap() error { return ErrTimeout }

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

// ClosedError is returned when the child exits before a wait's condition is
// met. It unwraps to ErrChildExited.
type ClosedError struct {
	// Op is the wait that failed, such as "WaitForText".
	Op string
	// Want describes the condition in words.
	Want string
	// ExitCode is the child's exit code, or -1 if it could not be determined.
	ExitCode int
	// Screen is the plain-text screen at the moment of the failure.
	Screen string
	// TailLog is the tail of the mirrored PTY I/O.
	TailLog string
}

// Unwrap makes errors.Is(err, ErrChildExited) true for every early exit.
func (e *ClosedError) Unwrap() error { return ErrChildExited }

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
				Screen:   t.viewLocked().Text(),
				TailLog:  t.tailLogLocked(),
			}
		}
		if !time.Now().Before(deadline) {
			return &TimeoutError{
				Op:      op,
				Want:    want,
				Elapsed: time.Since(start),
				Screen:  t.viewLocked().Text(),
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
//
// cond is called with the terminal's lock held, each time the program writes
// and every few milliseconds in between, so it must be quick and must not call
// back into the Terminal. The Screen it receives is immutable and may be kept.
//
// Like every wait, WaitFor treats a timeout of zero or less as one second, and
// returns an error wrapping ErrChildExited if the program exits before the
// condition holds, or ErrTimeout if time runs out. The error message carries
// the screen at that moment.
func (t *Terminal) WaitFor(cond func(Screen) bool, timeout time.Duration) error {
	return t.waitLoop("WaitFor", "custom condition", timeout, false, func() bool {
		return cond(t.viewLocked())
	})
}

// WaitForText blocks until the plain-text screen contains substr.
func (t *Terminal) WaitForText(substr string, timeout time.Duration) error {
	return t.waitLoop("WaitForText", fmt.Sprintf("text %q", substr), timeout, false, func() bool {
		return strings.Contains(t.viewLocked().Text(), substr)
	})
}

// WaitForMatch blocks until re matches within the given scope.
func (t *Terminal) WaitForMatch(re *regexp.Regexp, scope Scope, timeout time.Duration) error {
	return t.waitLoop("WaitForMatch", fmt.Sprintf("match %s", re.String()), timeout, false, func() bool {
		return re.MatchString(scopeText(t.viewLocked(), scope))
	})
}

// stabilizeInterval is the quiet window WaitStable and WaitForStable use.
func (t *Terminal) stabilizeInterval() time.Duration {
	if t.cfg.stabilize <= 0 {
		return DefaultStabilizeInterval
	}
	return t.cfg.stabilize
}

// WaitStable blocks until the terminal has been quiet for the stabilize
// interval (see WithStabilizeInterval), or until timeout. A child that has
// exited counts as stable.
//
// The quiet window is measured from the later of the last output byte and the
// last input tuitest sent. That matters: measured from output alone, calling
// WaitStable immediately after SendKeys would return against the pre-keystroke
// screen whenever the program had already been idle for the interval. Waiting
// out the window from the keystroke instead gives the program that long to
// start reacting, and any byte it produces restarts the window.
//
// A program that has not written anything yet is never stable. Starting a
// process takes long enough, on a loaded machine or under -race, that a quiet
// window measured from spawn used to elapse before the first byte arrived, and
// WaitStable then returned a blank screen. A program that never writes at all
// makes WaitStable time out.
//
// It is still a heuristic. A program that takes longer than the stabilize
// interval to react to input will be reported stable too early, and no
// quiescence rule can distinguish that from a program with nothing to say.
// Prefer WaitForText, WaitForMatch or WaitFor whenever the expected end state
// is known, and WaitForStable when the state is known but may still be drawing.
// Reach for WaitStable only after heavy output where it is not known at all.
func (t *Terminal) WaitStable(timeout time.Duration) error {
	quiet := t.stabilizeInterval()
	return t.waitLoop("WaitStable", fmt.Sprintf("output to quiesce for %s", quiet), timeout, true, func() bool {
		if t.exited {
			return true
		}
		if t.outBytes == 0 {
			return false
		}
		since := t.lastWrite
		if t.lastInput.After(since) {
			since = t.lastInput
		}
		return time.Since(since) >= quiet
	})
}

// WaitForStable blocks until cond holds on a screen whose content has not
// changed for the stabilize interval (see WithStabilizeInterval), or until
// timeout.
//
// It is for the case WaitFor handles badly: the text a test waits for is drawn
// before the rest of the frame it belongs to. A program redraws from the top
// down and the PTY delivers the frame in pieces, so the moment a marker near
// the top appears, the rows below it can still hold the previous frame, and an
// assertion on them made straight after WaitFor fails against a screen that is
// correct a moment later. WaitForStable waits for the frame to finish as well.
// It is also the way to read a value off the screen that is still moving, such
// as a counter or a scroll position: wait for it to stop, then read it.
//
// Only the content of the cells counts as a change: text, colours and
// attributes, and the size. Output that repaints the same content, and cursor
// movement, do not restart the window. As with WaitStable, the window is not
// considered to start before the last input tuitest sent, so a screen that
// already satisfied cond before a keystroke is not reported until the program
// has had the interval to react to it. A child that has exited cannot change
// the screen again, so once it has, cond holding is enough.
//
// Programs that draw inside synchronized updates (mode 2026) are already never
// seen half drawn, since the harness shows the previous frame until the update
// closes, the way a real terminal does. WaitForStable still helps with the ones
// that do not, and with a program that draws one logical frame in several
// updates.
func (t *Terminal) WaitForStable(cond func(Screen) bool, timeout time.Duration) error {
	quiet := t.stabilizeInterval()
	var (
		last  *screenSnapshot
		since time.Time
	)
	return t.waitLoop("WaitForStable", fmt.Sprintf("a condition to hold on a screen unchanged for %s", quiet), timeout, false, func() bool {
		s := t.viewLocked()
		switch {
		case last == nil:
			// Nothing has been drawn since the last byte arrived, so the screen
			// has looked like this at least since then.
			since = t.lastWrite
		case s != last && !sameContent(s, last):
			since = time.Now()
		}
		last = s
		if !cond(s) {
			return false
		}
		if t.exited {
			return true
		}
		from := since
		if t.lastInput.After(from) {
			from = t.lastInput
		}
		return time.Since(from) >= quiet
	})
}

// sameContent reports whether two snapshots show the same cells.
func sameContent(a, b *screenSnapshot) bool {
	if a.cols != b.cols || a.rows != b.rows || len(a.cells) != len(b.cells) {
		return false
	}
	for row := range a.cells {
		ra, rb := a.cells[row], b.cells[row]
		if len(ra) != len(rb) {
			return false
		}
		for col := range ra {
			if ra[col] != rb[col] {
				return false
			}
		}
	}
	return true
}

// WaitForOutput blocks until the child writes anything at all after the call
// begins, or it exits, or timeout elapses.
//
// This is the primitive for "wait until the program reacts to what I just
// sent", which WaitStable does not express: WaitStable asks whether output has
// been quiet for a window, and after a pause that is already true, so it
// returns immediately without the program having done anything. Use WaitStable
// to wait for a burst of output to finish, and WaitForOutput to wait for a
// reaction to begin. A child that has exited counts as settled, since no
// further output can arrive.
func (t *Terminal) WaitForOutput(timeout time.Duration) error {
	t.mu.Lock()
	base := t.outBytes
	t.mu.Unlock()
	return t.waitLoop("WaitForOutput", "the program to write something", timeout, true, func() bool {
		return t.exited || t.outBytes > base
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
// and whether one has been seen. It reports false both when no command has
// finished and when the terminal was started without WithSemanticMarkers, so
// enable that option before relying on it; the WaitForPrompt and WaitForCommand
// waits return ErrSemanticMarkers in that case and are the better signal.
func (t *Terminal) LastCommandExit() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.cfg.semantic {
		return 0, false
	}
	return t.emu.LastCommandExit()
}

func errSemanticDisabled(op string) error {
	return fmt.Errorf("%s: %w", op, ErrSemanticMarkers)
}
