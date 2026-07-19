package fuzz

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	// FailReplacementChar is U+FFFD reaching the screen, meaning the program
	// mangled a byte sequence somewhere between reading it and drawing it.
	FailReplacementChar FailureKind = "replacement-char"
	// FailInvariant is a user-supplied invariant returning an error. The
	// invariant's name is in Failure.Invariant, which keeps two different
	// invariants apart during shrinking and deduplication.
	FailInvariant FailureKind = "invariant"
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
	// Invariant names the user-supplied invariant that failed, for
	// FailInvariant. It is empty for every other kind.
	Invariant string
	// Onset is the index, into Commands, of the command after which the
	// failure was first observed. It is not the command at which it was
	// reported: an invariant is only reported once it has survived a wait, so
	// the two differ, and the earlier one is the one that points at the bug.
	// Zero means the failure carries no onset.
	Onset int
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
	// DetectReplacementChars enables the U+FFFD check. It is off by default
	// because the default generator sends malformed UTF-8, and against
	// malformed input a replacement character is the correct thing to draw
	// rather than a bug; see checkReplacementChars for the whole argument.
	DetectReplacementChars bool
}

// Invariant is a property of the screen the program is expected to maintain,
// supplied by the caller. Check returns nil while the property holds and an
// error describing the violation when it does not; the error text goes into the
// report, so it should say what was expected rather than only that something
// was wrong.
//
// An invariant is evaluated after every command but only reported once it has
// survived a wait, so a screen caught mid-redraw does not produce a finding.
// See monitor.reportInvariants.
type Invariant struct {
	// Name identifies the invariant in reports and, more importantly, to the
	// shrinker: minimisation only accepts a candidate that violates the same
	// named invariant, so two invariants failing in one session stay two
	// separate findings rather than collapsing into one.
	Name string
	// Check reports whether the property holds for this screen.
	Check func(tuitest.Screen) error
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

	// screenFn takes the current screen. It is a field rather than a direct
	// call on term so the invariant machinery, which is pure screen-reading,
	// can be exercised against scripted frames without spawning a program.
	screenFn func() tuitest.Screen

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

	// drawn records that the program has, at some point, rendered something.
	// Until it has, the grid is the blank screen the emulator started with and
	// no screen property can be judged against it. It latches on rather than
	// tracking the current screen, because a program that blanks the screen
	// after drawing has done something an invariant should be allowed to catch.
	drawn bool

	baseRSS uint64
	peakRSS uint64

	// cmdIndex is how many commands have been executed. It is the index a
	// failure's Onset is reported in, so it counts every command the player
	// was handed, including the ones that produced no input.
	cmdIndex int

	// inputWellFormed records that everything sent to the program so far has
	// been valid UTF-8 containing no replacement character of its own. The
	// U+FFFD check only means anything while this holds.
	inputWellFormed bool

	invariants []Invariant
	// invState[i] tracks invariants[i] across the run: whether it is currently
	// failing, since when, and with what error.
	invState []invariantState
}

// invariantState is one invariant's running state. onset is the command index
// at which the current failing streak began, which is what a report cites: a
// property that broke at command 12 and was noticed at command 40 is a bug
// about command 12, and it is what the shrinker should minimise toward.
type invariantState struct {
	failing bool
	onset   int
	err     error
}

func newMonitor(term *tuitest.Terminal, limits Limits, cols, rows int, invariants []Invariant) *monitor {
	bytes, _ := term.Progress()
	m := &monitor{
		limits:          limits,
		term:            term,
		wantCols:        cols,
		wantRows:        rows,
		lastBytes:       bytes,
		lastProgress:    time.Now(),
		screenFn:        term.Screen,
		inputWellFormed: true,
		invariants:      invariants,
		invState:        make([]invariantState, len(invariants)),
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
	m.cmdIndex++

	// Once malformed bytes have been delivered, a replacement character on
	// screen is no longer evidence of anything: drawing one is the correct
	// response to input that cannot be decoded. The same goes for a payload
	// that literally contains U+FFFD, which a program is entitled to echo.
	// Either poisons the check for the rest of the run rather than for one
	// command, because there is no bound on how long a program may hold input
	// before drawing it.
	if c.Text != "" && (!utf8.ValidString(c.Text) || strings.ContainsRune(c.Text, utf8.RuneError)) {
		m.inputWellFormed = false
	}

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
	if f := m.checkReplacement(); f != nil {
		return f
	}
	return m.checkMemory()
}

func (m *monitor) checkReplacement() *Failure {
	if !m.limits.DetectReplacementChars || !m.inputWellFormed {
		return nil
	}
	return checkReplacementChars(m.screenFn())
}

// checkReplacementChars reports U+FFFD on screen. Reaching the grid means the
// program decoded a byte sequence it could not represent and substituted the
// replacement character, or emitted one directly; either way the text the user
// is looking at is not the text the program meant to draw.
//
// The check is only sound while every byte sent to the program has been valid
// UTF-8, which is why the caller gates it on that and why it is off by default:
// the generator sends malformed UTF-8 by default (hostile bursts, and text
// truncated mid-rune one time in ten), and against those bytes a replacement
// character is the correct output, not a bug. Turning the check on is therefore
// usually paired with Config.NoHostile, and even then the generator's mid-rune
// truncation will poison some runs. What is left is the case worth having: a
// program handed well-formed text that mangles it anyway, which is the common
// shape of a fixed-size read buffer cutting a rune in half.
//
// It reads the rendered line rather than the cells so that a concealed cell,
// which a real terminal draws as a blank, is not reported as a character the
// user can see.
func checkReplacementChars(sc tuitest.Screen) *Failure {
	_, rows := sc.Size()
	for row := 0; row < rows; row++ {
		line := sc.Line(row)
		col := strings.IndexRune(line, utf8.RuneError)
		if col < 0 {
			continue
		}
		return &Failure{
			Kind: FailReplacementChar,
			Detail: fmt.Sprintf("U+FFFD at row %d, column %d, after only well-formed input: %q",
				row, utf8.RuneCountInString(line[:col]), line),
			Screen: sc.Text(),
		}
	}
	return nil
}

// observeInvariants evaluates every invariant against the current screen and
// updates the failing streaks. It deliberately does not report: a screen caught
// between the erase and the repaint of a frame will fail a reasonable
// invariant, and reporting here would bury a real finding under those. What it
// does instead is remember when each streak started, so that when
// reportInvariants does fire the index it cites is the command that broke the
// property rather than the one that happened to be running when the checker
// looked.
func (m *monitor) observeInvariants() {
	if len(m.invariants) == 0 {
		return
	}
	sc := m.screenFn()
	// Nothing has been drawn yet, so the grid is the blank screen the emulator
	// started with and every reasonable invariant fails against it. Judging one
	// here would report the harness's own startup, and worse, would hand the
	// shrinker a violation reachable from Spawn alone, which it would then
	// minimise every finding down to. That is not hypothetical: it is what the
	// shrinker did the first time this ran without the gate.
	if !m.drawn {
		if sc.Text() == "" {
			return
		}
		m.drawn = true
	}
	for i := range m.invariants {
		err := m.invariants[i].Check(sc)
		st := &m.invState[i]
		switch {
		case err == nil:
			// Recovered, so the streak was a redraw in progress and not a bug.
			*st = invariantState{}
		case !st.failing:
			*st = invariantState{failing: true, onset: m.cmdIndex, err: err}
		default:
			// Keep the original onset; refresh the error so the report
			// describes the violation as it currently stands.
			st.err = err
		}
	}
}

// reportInvariants turns a failing streak into a finding, and is called only at
// a settle: after a WaitStable command, and after the end-of-iteration settle.
// That gating is the whole false-positive defence. The fuzzer sends input far
// faster than a program consumes it, so between two ordinary commands the screen
// is routinely a half-drawn frame; a settle is the only point at which the
// program has been given the chance to finish, and a property still violated
// after one is violated for real.
//
// final distinguishes the two callers, and the difference is not cosmetic.
// Mid-run, a streak that began at this very command is not reported: a tape's
// own wait returning is weaker evidence than the iteration's settle, so the
// streak is given until the next settle to clear. At the end of the iteration
// there is no next settle, and the harness has just quiesced the program
// itself, so a streak first seen there has already had its chance. Withholding
// it there was tried and was wrong: it silently dropped every finding whose
// redraw landed during the settle, which is most of the small reproductions
// minimisation produces.
func (m *monitor) reportInvariants(final bool) *Failure {
	for i := range m.invariants {
		st := m.invState[i]
		if !st.failing {
			continue
		}
		if !final && st.onset >= m.cmdIndex {
			continue
		}
		when := fmt.Sprintf("was still failing at command %d", m.cmdIndex)
		if final {
			when = "was still failing once the program had settled"
		}
		return &Failure{
			Kind:      FailInvariant,
			Invariant: m.invariants[i].Name,
			Onset:     st.onset,
			Detail: fmt.Sprintf("invariant %q first failed after command %d and %s: %v",
				m.invariants[i].Name, st.onset, when, st.err),
			Screen: m.screenFn().Text(),
		}
	}
	return nil
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
		Screen: m.screenFn().Text(),
	}
}

func (m *monitor) checkScreen() *Failure {
	return checkScreenModel(m.screenFn(), m.wantCols, m.wantRows)
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
		Screen: m.screenFn().Text(),
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
		Screen: m.screenFn().Text(),
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
		Screen: m.screenFn().Text(),
	}
}
