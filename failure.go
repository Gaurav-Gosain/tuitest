package tuitest

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// ErrEmulatorPanic is wrapped by every call made on a terminal whose VT
// emulator panicked while it read the program's output.
var ErrEmulatorPanic = errors.New("tuitest: the terminal emulator panicked")

// EmulatorPanicError is returned by every wait, every input call and Resize
// once the emulator has panicked. It unwraps to ErrEmulatorPanic.
//
// The panic usually happens on the goroutine that reads the program's output,
// where no test can catch it. Left alone it ends the whole test binary: every
// other result is lost and no cleanup runs, so the programs the tests started
// are left running. The terminal recovers it instead, keeps reading the
// program's output so the program does not block on a full PTY, and stops
// feeding that output to the emulator, whose state can no longer be trusted.
// A panic in a resize or in a read of the screen is recovered the same way.
//
// The calls that return no error (Screen, Snapshot, TermState and
// LastCommandExit) return the state the emulator had when it panicked. They do
// not read the emulator again.
type EmulatorPanicError struct {
	// Op is the call that failed, such as "WaitForText" or "Type".
	Op string
	// During says what the emulator was doing when it panicked, such as
	// "reading the program's output" or "resizing to 80x24".
	During string
	// Value is what the emulator panicked with.
	Value any
	// Stack is the stack of the goroutine that panicked.
	Stack string
	// Chunk is the output the emulator was reading when it panicked. It is
	// empty when the panic came from something other than the output.
	Chunk []byte
	// Screen is the plain-text screen when the emulator panicked.
	Screen string
	// TailLog is the tail of the mirrored PTY I/O.
	TailLog string
}

// Unwrap makes errors.Is(err, ErrEmulatorPanic) true.
func (e *EmulatorPanicError) Unwrap() error { return ErrEmulatorPanic }

// maxChunkInError bounds how much of the chunk the error message quotes.
const maxChunkInError = 512

func (e *EmulatorPanicError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tuitest: %s failed: the terminal emulator panicked while %s: %v", e.Op, e.During, e.Value)
	if len(e.Chunk) > 0 {
		b.WriteString("\nThis is a bug in tuitest. Report it with the chunk and the stack below.")
	} else {
		b.WriteString("\nThis is a bug in tuitest. Report it with the stack below.")
	}
	if e.Screen != "" {
		b.WriteString("\n--- screen ---\n")
		b.WriteString(e.Screen)
	}
	switch chunk := e.Chunk; {
	case len(chunk) > maxChunkInError:
		chunk = chunk[len(chunk)-maxChunkInError:]
		fmt.Fprintf(&b, "\n--- chunk (last %d of %d bytes) ---\n%q", maxChunkInError, len(e.Chunk), chunk)
	case len(chunk) > 0:
		fmt.Fprintf(&b, "\n--- chunk ---\n%q", chunk)
	}
	if e.TailLog != "" {
		b.WriteString("\n--- last I/O ---\n")
		b.WriteString(e.TailLog)
	}
	if e.Stack != "" {
		b.WriteString("\n--- stack ---\n")
		b.WriteString(e.Stack)
	}
	return b.String()
}

// emuPanic records a recovered emulator panic. Only screen changes after it is
// set, to carry the current exit state. See panicViewLocked.
type emuPanic struct {
	value  any
	during string
	stack  string
	chunk  []byte
	screen *screenSnapshot // the grid when the emulator panicked
	// The state the reads that return no error report from now on.
	termState  TermState
	lastExit   int
	lastExitOK bool
}

// feedLocked hands a chunk of output to the emulator and recovers a panic in
// it. Caller holds t.mu.
func (t *Terminal) feedLocked(p []byte) {
	t.emuLocked("reading the program's output", p, func() {
		_, _ = t.emu.Write(p)
		t.gen++
		t.queueResponses(t.emu.TakeResponses())
	})
}

// emuLocked runs fn, a call into the emulator, and recovers a panic in it. It
// reports false, and does not run fn, once the emulator has panicked. during
// names what fn does, and chunk is the output fn hands to the emulator, if
// any. Caller holds t.mu.
//
// Every call into the emulator goes through it, except the calls made while
// one is already running (syncChangedLocked runs inside Write) and the reads
// that salvage state after a panic, which recover on their own. A panic left
// to reach a test goroutine would leave t.mu held where no deferred unlock
// releases it, and a panic on the pump goroutine would end the test binary.
func (t *Terminal) emuLocked(during string, chunk []byte, fn func()) (ok bool) {
	if t.panicked != nil {
		return false
	}
	defer func() {
		if v := recover(); v != nil {
			t.emulatorPanickedLocked(v, during, chunk)
			ok = false
		}
	}()
	fn()
	return true
}

// emulatorPanickedLocked marks the terminal broken. It runs in the deferred
// function that recovered the panic, so debug.Stack still holds the frames
// that panicked. Caller holds t.mu.
func (t *Terminal) emulatorPanickedLocked(v any, during string, p []byte) {
	ep := &emuPanic{
		value:  v,
		during: during,
		stack:  string(debug.Stack()),
		chunk:  append([]byte(nil), p...),
	}
	ep.screen = t.salvageScreenLocked()
	// A read that panics again leaves the zero value.
	tryLocked(func() { ep.termState = t.termStateLocked() })
	tryLocked(func() { ep.lastExit, ep.lastExitOK = t.emu.LastCommandExit() })
	t.panicked = ep
	t.presented = nil
}

// tryLocked runs fn and swallows a panic in it. It is for reads of an emulator
// that has already panicked once.
func tryLocked(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// salvageScreenLocked returns the best screen there is after a panic: the grid
// as it stands if it can still be read, or else the last screen read before
// the panic, or else a blank one. Caller holds t.mu.
func (t *Terminal) salvageScreenLocked() (s *screenSnapshot) {
	code, exited := t.exitLocked()
	defer func() {
		if recover() == nil {
			return
		}
		switch {
		case t.presented != nil:
			s = t.presented
		case t.cache != nil:
			s = t.cache
		default:
			s = blankSnapshot(t.cfg.cols, t.cfg.rows)
		}
	}()
	return t.buildSnapshotLocked(code, exited)
}

func blankSnapshot(cols, rows int) *screenSnapshot {
	return (&screenSnapshot{exitCode: -1}).resized(cols, rows)
}

// panicViewLocked returns the screen frozen at the panic, with the current exit
// state. Caller holds t.mu.
func (t *Terminal) panicViewLocked() *screenSnapshot {
	s := t.panicked.screen
	code, exited := t.exitLocked()
	if s.exited == exited && s.exitCode == code {
		return s
	}
	// The cells are never changed after a snapshot is built, so the new one
	// can share them.
	s = &screenSnapshot{
		cols: s.cols, rows: s.rows, cells: s.cells,
		curCol: s.curCol, curRow: s.curRow, curVisible: s.curVisible,
		exitCode: code, exited: exited,
	}
	t.panicked.screen = s
	return s
}

// panicErrorLocked returns the error op fails with after a panic, or nil.
// Caller holds t.mu.
func (t *Terminal) panicErrorLocked(op string) error {
	ep := t.panicked
	if ep == nil {
		return nil
	}
	return &EmulatorPanicError{
		Op:      op,
		During:  ep.during,
		Value:   ep.value,
		Stack:   ep.stack,
		Chunk:   ep.chunk,
		Screen:  t.panicViewLocked().Text(),
		TailLog: t.tailLogLocked(),
	}
}

// DefaultWriteTimeout is how long input may wait for the program to read it,
// unless WithWriteTimeout sets another limit.
const DefaultWriteTimeout = 10 * time.Second

// inputClosedError is returned by input sent after an earlier write timed out.
// It unwraps to that write's *TimeoutError, and so to ErrTimeout.
type inputClosedError struct {
	op    string
	first *TimeoutError
}

func (e *inputClosedError) Error() string {
	return fmt.Sprintf("tuitest: %s refused: an earlier %s timed out, and part of its input can be in the program's input buffer. The terminal accepts no more input.",
		e.op, e.first.Op)
}

func (e *inputClosedError) Unwrap() error { return e.first }

// unusableError returns the error input fails with when the terminal can no
// longer take input: the emulator panicked, or an earlier write timed out.
func (t *Terminal) unusableError(op string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.panicErrorLocked(op); err != nil {
		return err
	}
	if t.stalled != nil {
		return &inputClosedError{op: op, first: t.stalled}
	}
	return nil
}

// sendInput writes b to the program, after any query answers the terminal
// still owes it.
func (t *Terminal) sendInput(b []byte) error {
	t.inputMu.Lock()
	defer t.inputMu.Unlock()
	// Answers the terminal owes the program go first: they were produced
	// before this input was, and a real terminal delivers them in that order.
	if q := t.takeResponsesLocked(); len(q) > 0 {
		b = append(q, b...)
	}
	err := t.proc.Write(b)
	if err == nil {
		t.inBytes.Add(int64(len(b)))
	}
	return err
}

// sendInputBounded is sendInput with the write timeout. A program that stops
// reading its input fills the PTY's input buffer, and a write to a full buffer
// blocks until the program reads. Without a bound, the test blocks there until
// go test kills the binary, with no screen and no cleanup.
//
// On timeout the write is left running: there is no way to take back the part
// of it the kernel has already accepted. That part can end in the middle of an
// escape sequence, so any later input would reach the program corrupted. The
// terminal therefore refuses all later input.
//
// Close does not end the write. On Linux a write to a PTY master whose input
// buffer is full stays blocked after the program is gone, and Go defers the
// close of a descriptor that a blocked call still uses. So one goroutine, its
// buffer and the PTY descriptor stay until the test binary exits. Close still
// returns, and the test still fails with the screen.
func (t *Terminal) sendInputBounded(op string, b []byte) error {
	timeout := t.cfg.writeTimeout
	if timeout <= 0 {
		return t.sendInput(b)
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- t.sendInput(b) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		// The write can finish just as the timer fires. Then it did not
		// time out.
		select {
		case err := <-done:
			return err
		default:
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	te := &TimeoutError{
		Op:      op,
		Want:    fmt.Sprintf("the program to read %d bytes of input", len(b)),
		Elapsed: time.Since(start),
		Screen:  t.viewLocked().Text(),
		TailLog: t.tailLogLocked(),
	}
	t.stalled = te
	return te
}
