package tape

import (
	"errors"
	"io"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// Default timing for a recording session.
const (
	// DefaultQuiet is how long the screen must hold still before a settle point
	// is declared.
	DefaultQuiet = 120 * time.Millisecond
	// DefaultSettleMax bounds how long a single settle point waits, so a
	// program that redraws forever (a clock, a spinner) does not stall the
	// recording.
	DefaultSettleMax = 2 * time.Second
	// settlePoll is how often the screen is sampled while settling.
	settlePoll = 10 * time.Millisecond
)

// Size is a terminal size in cells.
type Size struct{ Cols, Rows int }

// Session records one interactive run: it spawns the program in a PTY, passes
// input through to it, renders its output to Out, and feeds a Recorder.
//
// The plumbing is deliberately parameterized rather than reaching for os.Stdin
// and os.Stdout, so the whole record path is exercised by tests with a scripted
// input stream instead of needing a real terminal.
type Session struct {
	Argv []string
	// In supplies raw terminal input. Recording ends when it reaches EOF.
	In io.Reader
	// Out receives the program's output, normally the operator's terminal.
	Out io.Writer
	// Resizes, when non-nil, delivers terminal size changes.
	Resizes <-chan Size

	Cols, Rows int
	Term       string
	Env        []string

	// Quiet and SettleMax tune settle detection; zero means the defaults.
	Quiet     time.Duration
	SettleMax time.Duration

	// StopKey ends the recording when it appears in the input, without being
	// forwarded to the program. Zero disables it, leaving EOF and program exit
	// as the only ways to stop.
	StopKey byte

	// Recorder receives the events. A nil Recorder gets a default one.
	Recorder *Recorder
}

// readerChunk is one read from the input stream.
type readerChunk struct {
	data []byte
	err  error
}

// Run records the session and returns the resulting tape. It always tears down
// the spawned program before returning, including on error.
func (s *Session) Run() ([]Command, error) {
	if len(s.Argv) == 0 {
		return nil, errors.New("tape: record needs a program to run")
	}
	rec := s.Recorder
	if rec == nil {
		rec = NewRecorder()
	}

	cols, rows := s.Cols, s.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	term := s.Term
	if term == "" {
		term = "xterm-256color"
	}
	quiet := s.Quiet
	if quiet <= 0 {
		quiet = DefaultQuiet
	}
	settleMax := s.SettleMax
	if settleMax <= 0 {
		settleMax = DefaultSettleMax
	}

	rec.Header(cols, rows, term, s.Env, s.Argv)

	opts := []tuitest.Option{
		tuitest.WithSize(cols, rows),
		tuitest.WithTerm(term),
	}
	if len(s.Env) > 0 {
		opts = append(opts, tuitest.WithEnv(s.Env...))
	}
	if s.Out != nil {
		opts = append(opts, tuitest.WithOutputMirror(s.Out))
	}
	tt, err := tuitest.Start(s.Argv, opts...)
	if err != nil {
		return nil, err
	}
	defer tt.Close() //nolint:errcheck

	// A dedicated reader goroutine so input, program exit, and resizes can all
	// be selected on. It exits when the input stream ends.
	chunks := make(chan readerChunk)
	done := make(chan struct{})
	defer close(done)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.In.Read(buf)
			var c readerChunk
			if n > 0 {
				c.data = append([]byte(nil), buf[:n]...)
			}
			c.err = err
			select {
			case chunks <- c:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// The program's opening screen is the first settle point.
	last := time.Now()
	res := s.settleOnce(tt, quiet, settleMax, chunks, rec)
	rec.Settle("", res.text, time.Since(last))
	before := res.text
	carry := res.pending
	stopped := res.stop

	for !stopped {
		var chunk []byte
		if carry != nil {
			chunk, carry = carry, nil
		} else {
			select {
			case c := <-chunks:
				if len(c.data) == 0 && c.err != nil {
					stopped = true
					continue
				}
				chunk = c.data
				if c.err != nil {
					stopped = true
				}
			case sz := <-s.Resizes:
				s.applyResize(tt, rec, sz)
				continue
			case <-tt.Done():
				stopped = true
				continue
			}
		}

		if idx := s.stopIndex(chunk); idx >= 0 {
			chunk = chunk[:idx]
			stopped = true
		}
		if len(chunk) == 0 {
			continue
		}

		start := time.Now()
		// The modes the program has set determine how its input is read, so
		// they are sampled from the live terminal immediately before the input
		// that arrived under them is decoded.
		st := tt.TermState()
		rec.SetModes(NewModes(st.Mode))
		rec.Input(chunk)
		if err := tt.Type(string(chunk)); err != nil {
			// The program is gone; stop cleanly rather than reporting a write
			// error for input the operator already typed.
			break
		}

		res = s.settleOnce(tt, quiet, settleMax, chunks, rec)
		rec.Settle(before, res.text, time.Since(start))
		before = res.text
		carry = res.pending
		if res.stop {
			stopped = true
		}
	}

	// Recording stopped, but the program may still be drawing its last frame or
	// on its way out. Settle once more so the tape ends on the state the
	// operator actually saw, and so a program that exits in response to the
	// final keystroke gets its exit asserted.
	tail := time.Now()
	final := s.finalSettle(tt, quiet, settleMax)
	rec.Settle(before, final, time.Since(tail))

	if code, exited := tt.ExitCode(); exited {
		rec.ExpectExit(code)
	}
	return rec.Commands(), nil
}

// finalSettle waits for the screen to hold still after recording stops. Unlike
// settleOnce it ignores further input, and once the screen is quiet it gives a
// child that is already exiting the chance to finish: the last keystroke of a
// session is usually the one that quits the program, and its farewell output
// belongs in the tape.
func (s *Session) finalSettle(tt *tuitest.Terminal, quiet, settleMax time.Duration) string {
	last := tt.Snapshot()
	quietSince := time.Now()
	deadline := time.Now().Add(settleMax)

	tick := time.NewTicker(settlePoll)
	defer tick.Stop()

	for time.Now().Before(deadline) {
		<-tick.C
		cur := tt.Snapshot()
		if cur != last {
			last = cur
			quietSince = time.Now()
			continue
		}
		if time.Since(quietSince) < quiet {
			continue
		}
		// The screen held still for the whole quiet window, so any farewell
		// output from a child that is on its way out has already been drawn and
		// its exit code is readable by the time the caller asserts on it.
		return cur
	}
	return tt.Snapshot()
}

// stopIndex finds the stop key in a chunk, or -1.
func (s *Session) stopIndex(chunk []byte) int {
	if s.StopKey == 0 {
		return -1
	}
	for i, b := range chunk {
		if b == s.StopKey {
			return i
		}
	}
	return -1
}

// settleResult is the outcome of waiting for the screen to hold still.
type settleResult struct {
	text    string // screen once it settled
	pending []byte // input that arrived while settling, not yet handled
	stop    bool   // the input stream ended or the program exited
}

// applyResize resizes the live PTY and records the change.
func (s *Session) applyResize(tt *tuitest.Terminal, rec *Recorder, sz Size) {
	if sz.Cols <= 0 || sz.Rows <= 0 {
		return
	}
	if err := tt.Resize(sz.Cols, sz.Rows); err != nil {
		return
	}
	rec.Resize(sz.Cols, sz.Rows)
}

// settleOnce samples the screen until it stops changing for the quiet window,
// the deadline passes, or new input arrives. Input wins: further typing means
// the burst is not over, and grouping it into one settle point is what keeps a
// fast typist's recording from becoming one Wait per character.
func (s *Session) settleOnce(tt *tuitest.Terminal, quiet, settleMax time.Duration, chunks <-chan readerChunk, rec *Recorder) settleResult {
	last := tt.Snapshot()
	quietSince := time.Now()
	deadline := time.Now().Add(settleMax)

	tick := time.NewTicker(settlePoll)
	defer tick.Stop()

	for {
		select {
		case c := <-chunks:
			if len(c.data) == 0 && c.err != nil {
				return settleResult{text: tt.Snapshot(), stop: true}
			}
			return settleResult{text: tt.Snapshot(), pending: c.data, stop: c.err != nil}

		case sz := <-s.Resizes:
			s.applyResize(tt, rec, sz)
			// A resize repaints, so restart the quiet window.
			quietSince = time.Now()

		case <-tt.Done():
			return settleResult{text: tt.Snapshot(), stop: true}

		case <-tick.C:
			cur := tt.Snapshot()
			if cur != last {
				last = cur
				quietSince = time.Now()
			} else if time.Since(quietSince) >= quiet {
				return settleResult{text: cur}
			}
			if time.Now().After(deadline) {
				return settleResult{text: cur}
			}
		}
	}
}
