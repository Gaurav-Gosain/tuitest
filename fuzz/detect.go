package fuzz

import (
	"fmt"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// FailureKind classifies what went wrong. Shrinking compares kinds to decide
// whether a reduced input still reproduces "the same" bug, so kinds must be
// coarse enough to survive minimisation but fine enough to keep distinct bugs
// apart.
type FailureKind string

const (
	// FailCrash is the program dying from a fault signal or exiting non-zero.
	FailCrash FailureKind = "crash"
	// FailHang is the program going unresponsive: still alive, but producing no
	// output for longer than the hang bound while input keeps arriving.
	FailHang FailureKind = "hang"
	// FailScreenInconsistent is the screen model contradicting itself, such as
	// the cursor sitting outside the grid or the grid changing size on its own.
	FailScreenInconsistent FailureKind = "screen-inconsistent"
	// FailDirtyExit is the program exiting without restoring the terminal.
	FailDirtyExit FailureKind = "dirty-terminal"
	// FailMemoryGrowth is resident memory climbing past the configured ceiling.
	FailMemoryGrowth FailureKind = "memory-growth"
)

// Failure is one reproducible finding.
type Failure struct {
	Kind FailureKind
	// Detail explains the specific violation, for the tape header and report.
	Detail string
	// Commands is the input that produced it, in tape form.
	Commands []tape.Command
	// Screen is the plain-text screen at the moment of detection.
	Screen string
	// Seed and Iteration identify the run that found it.
	Seed      uint64
	Iteration int
	// Shrunk records how many commands the original failing input had, so a
	// report can show what minimisation achieved.
	Original int
	// Verified records whether the minimised input reproduced on a final
	// confirmation run. A repro that does not re-verify is still reported, but
	// labelled, because a flaky finding is worth less than a solid one.
	Verified bool
}

func (f *Failure) Error() string {
	return fmt.Sprintf("%s: %s", f.Kind, f.Detail)
}

// Limits bounds what the detectors treat as acceptable.
type Limits struct {
	// HangAfter is how long the program may produce no output at all, while
	// input keeps arriving and it is still alive, before it counts as hung.
	HangAfter time.Duration
	// MaxRSSGrowth is the factor resident memory may grow by, relative to the
	// reading taken once the program settled, before it counts as a leak. Zero
	// disables the check.
	MaxRSSGrowth float64
	// MinRSSBytes is the floor below which growth is ignored, so a program
	// starting at a few hundred kilobytes does not trip the ratio on noise.
	MinRSSBytes uint64
	// AllowDirtyExit suppresses the terminal-restoration check, for programs
	// that are not full-screen and never claimed to restore anything.
	AllowDirtyExit bool
}

// DefaultLimits are tuned to be quiet on well-behaved programs. The hang bound
// is generous because a TUI legitimately sits silent when it ignores input;
// only total silence for seconds, with input still arriving, is evidence.
func DefaultLimits() Limits {
	return Limits{
		HangAfter:    5 * time.Second,
		MaxRSSGrowth: 0, // off by default: too noisy to enable unasked
		MinRSSBytes:  16 << 20,
	}
}

// monitor watches one spawned program across an iteration.
type monitor struct {
	limits  Limits
	term    *tuitest.Terminal
	sampler *rssSampler

	// wantCols and wantRows are the size we last asked for. The screen-model
	// check compares against these, so a grid that changes size without a
	// resize is caught.
	wantCols, wantRows int

	// lastProgress is the output-byte count and wall time at the last point the
	// program was observed making progress, and inputsSinceOutput counts the
	// input events delivered since then.
	lastBytes         int64
	lastProgress      time.Time
	inputsSinceOutput int

	baseRSS uint64
	peakRSS uint64
}

func newMonitor(term *tuitest.Terminal, limits Limits, cols, rows int) *monitor {
	bytes, _ := term.Progress()
	m := &monitor{
		limits:       limits,
		term:         term,
		wantCols:     cols,
		wantRows:     rows,
		lastBytes:    bytes,
		lastProgress: time.Now(),
	}
	if limits.MaxRSSGrowth > 0 {
		m.sampler = newRSSSampler(term.Pid())
		m.baseRSS = m.sampler.sample()
		m.peakRSS = m.baseRSS
	}
	return m
}

// noteCommand records what a command did that the detectors need to know:
// whether it changed the requested size, and whether it delivered input the
// program could have responded to.
func (m *monitor) noteCommand(c tape.Command) {
	switch c.Kind {
	case tape.KindResize:
		m.wantCols, m.wantRows = c.Cols, c.Rows
		// A resize is input too: it delivers SIGWINCH, and a program that stops
		// responding to one is exactly the degenerate-size bug worth finding.
		m.inputsSinceOutput++
	case tape.KindKey, tape.KindMouse:
		m.inputsSinceOutput++
	case tape.KindType, tape.KindRaw, tape.KindPaste:
		// An empty payload writes no bytes, so the program had nothing to
		// respond to and ignoring it proves nothing. Shrinking reduces payloads
		// toward empty, and counting those would let it produce a repro whose
		// commands no longer deliver the input the failure depends on.
		if c.Text != "" {
			m.inputsSinceOutput++
		}
	}
}

// check runs the detectors that can be judged instantly, between commands. Hang
// detection is deliberately not among them: it needs to wait, so it lives in
// checkHang and runs once per iteration from the settle phase.
func (m *monitor) check() *Failure {
	m.observeProgress()
	if f := m.checkExit(); f != nil {
		return f
	}
	if f := m.checkScreen(); f != nil {
		return f
	}
	return m.checkMemory()
}

// observeProgress notes whether the program has written anything since the last
// look. Any output at all means it answered the input sent so far, so the
// unanswered-input counter resets.
func (m *monitor) observeProgress() {
	bytes, _ := m.term.Progress()
	if bytes != m.lastBytes {
		m.lastBytes = bytes
		m.lastProgress = time.Now()
		m.inputsSinceOutput = 0
	}
}

// checkExit reports a crash. A clean zero exit is not a failure: the fuzzer
// sends keys that legitimately quit a program, and treating that as a bug would
// make every run a false positive.
func (m *monitor) checkExit() *Failure {
	st, exited := m.term.ExitStatus()
	if !exited || !st.Crashed() {
		return nil
	}
	return &Failure{
		Kind:   FailCrash,
		Detail: fmt.Sprintf("program %s", st),
		Screen: m.term.Screen().Text(),
	}
}

func (m *monitor) checkScreen() *Failure {
	return checkScreenModel(m.term.Screen(), m.wantCols, m.wantRows)
}

// checkScreenModel validates a screen against itself, given the size that was
// last requested. It is a free function so it can be exercised directly.
//
// Two invariants. The cursor must sit inside the grid: a hostile escape
// sequence that drives the cursor out of bounds is a live emulator bug, and
// this is the arm that catches it. The grid must also still be the size that
// was last asked for, because only a Resize can change it; that arm is a
// self-check on the harness's screen model rather than on the program under
// test, since this emulator does not honour program-driven resize sequences.
func checkScreenModel(sc tuitest.Screen, wantCols, wantRows int) *Failure {
	cols, rows := sc.Size()

	if cols != wantCols || rows != wantRows {
		return &Failure{
			Kind: FailScreenInconsistent,
			Detail: fmt.Sprintf("grid is %dx%d but the last requested size was %dx%d, with no resize in between",
				cols, rows, wantCols, wantRows),
			Screen: sc.Text(),
		}
	}

	col, row, _ := sc.Cursor()
	if col < 0 || row < 0 || col >= cols || row >= rows {
		return &Failure{
			Kind:   FailScreenInconsistent,
			Detail: fmt.Sprintf("cursor at (%d,%d) is outside the %dx%d grid", col, row, cols, rows),
			Screen: sc.Text(),
		}
	}
	return nil
}

// minIgnoredInputs is how many input events must go unanswered before silence
// counts as a hang. A program is allowed to ignore input: a TUI that discards a
// key it does not bind correctly writes nothing at all. Requiring several
// ignored events, rather than one, is what separates "had nothing to say" from
// "stopped listening".
const minIgnoredInputs = 3

// checkHang reports a program that is alive and has stopped answering input. It
// is called once, at the end of an iteration, and it waits.
//
// Waiting is the whole point. The question "is this program hung?" cannot be
// answered from a clock reading taken microseconds after sending input, when
// the program has not been scheduled yet. Nor can it be answered from
// accumulated silence, because silence also accrues while nobody is talking to
// the program, and an earlier version of this check did exactly that and
// reported healthy idle programs as hung. So: if input is outstanding, give the
// program the full grace period to answer, and only then call it hung.
//
// The cost is bounded and only paid when something is actually wrong. A
// responsive program answers immediately and the wait returns at once.
func (m *monitor) checkHang() *Failure {
	if _, exited := m.term.ExitStatus(); exited {
		return nil
	}
	m.observeProgress()
	if m.inputsSinceOutput < minIgnoredInputs {
		// Nothing meaningful is outstanding, so silence proves nothing.
		return nil
	}

	// WaitForOutput succeeds on any output, and also on the child exiting,
	// either of which means the program was not wedged.
	start := time.Now()
	if err := m.term.WaitForOutput(m.limits.HangAfter); err == nil {
		m.observeProgress()
		return nil
	}
	if _, exited := m.term.ExitStatus(); exited {
		return nil
	}

	return &Failure{
		Kind: FailHang,
		Detail: fmt.Sprintf("still running but produced no output for %s after %d unanswered input events",
			time.Since(start).Round(time.Millisecond), m.inputsSinceOutput),
		Screen: m.term.Screen().Text(),
	}
}

// checkMemory reports resident memory climbing past the configured ceiling.
// It is off unless MaxRSSGrowth is set, because a program that legitimately
// caches as it runs will trip any fixed threshold eventually.
func (m *monitor) checkMemory() *Failure {
	if m.sampler == nil || m.limits.MaxRSSGrowth <= 0 {
		return nil
	}
	rss := m.sampler.sample()
	if rss == 0 {
		return nil
	}
	if rss > m.peakRSS {
		m.peakRSS = rss
	}
	if m.baseRSS == 0 {
		m.baseRSS = rss
		return nil
	}
	if rss < m.limits.MinRSSBytes {
		return nil
	}
	growth := float64(rss) / float64(m.baseRSS)
	if growth < m.limits.MaxRSSGrowth {
		return nil
	}
	return &Failure{
		Kind: FailMemoryGrowth,
		Detail: fmt.Sprintf("resident memory grew from %d to %d bytes (%.1fx, ceiling %.1fx)",
			m.baseRSS, rss, growth, m.limits.MaxRSSGrowth),
		Screen: m.term.Screen().Text(),
	}
}

// checkExitState runs after the program has exited and reports a terminal left
// in a state that would damage the user's shell. This is the highest-value
// check in practice: it is a real bug class, it is common, and unlike the
// others it has almost no false-positive surface, because a program that turned
// a mode on is unambiguously responsible for turning it off.
func (m *monitor) checkExitState() *Failure {
	if m.limits.AllowDirtyExit {
		return nil
	}
	if _, exited := m.term.ExitStatus(); !exited {
		return nil
	}
	state := m.term.TermState()
	if !state.Dirty() {
		return nil
	}
	return &Failure{
		Kind:   FailDirtyExit,
		Detail: "program exited without restoring the terminal: " + state.Describe(),
		Screen: m.term.Screen().Text(),
	}
}
