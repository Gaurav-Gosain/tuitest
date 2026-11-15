package tuitest

import (
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/internal/ptyproc"
)

// process is what a Terminal needs from the program it drives: a child on a
// PTY (ptyproc.Process) or a Program that runs in the test process
// (inProcess). Everything above it, the emulator, the waits, the key encoding
// and the log, is the same for both.
type process interface {
	Write(b []byte) error
	Resize(cols, rows int) error
	Done() <-chan struct{}
	ExitCode() (int, bool)
	ExitStatus() (ptyproc.Status, bool)
	Pid() int
	Close() error
}

// Program is a terminal program that runs inside the test process instead of
// as a child on a PTY. StartProgram puts it behind a Terminal, and the
// Terminal treats its output and input exactly as it treats a PTY child's: the
// same emulator reads the output, and the same waits, screen reads and key
// encoding apply. A test written against a Terminal runs against either.
//
// There is no line discipline in between. The program reads the bytes a
// terminal sends, as a program in raw mode does, and what it writes reaches
// the screen as it is, so it writes "\r\n" to start a new line.
type Program interface {
	// Run runs the program until it ends and returns its exit code. It reads
	// its input from in and writes its output to out, and starts at a
	// terminal of cols by rows cells. in reports io.EOF once the Terminal
	// is closed. A panic in Run ends the program with exit code 2 and the
	// panic on the screen, as it would for a Go program on a PTY.
	Run(in io.Reader, out io.Writer, cols, rows int) int
	// Resize tells the program that its terminal is now cols by rows. It is
	// called from the test's goroutine while Run runs.
	Resize(cols, rows int)
	// Stop asks the program to end. Run must return soon after. Close calls
	// it, and it may be called after Run has returned.
	Stop()
}

// StartProgram runs prog in the test process behind a Terminal.
//
// The options that shape a child process do not apply: WithEnv,
// WithInheritEnv, WithDir, WithTerm and WithTrueColor are ignored, since the
// program has no environment or directory of its own. WithKillGrace bounds
// how long Close waits for Run to return after Stop.
func StartProgram(prog Program, opts ...Option) (*Terminal, error) {
	if prog == nil {
		return nil, errors.New("tuitest: StartProgram: the program is nil")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if err := checkSize("WithSize", cfg.cols, cfg.rows); err != nil {
		return nil, err
	}
	t := newTerminal(cfg)
	p := newInProcess(prog, t.onData, t.onClose, cfg.cols, cfg.rows, cfg.killGrace)
	t.mu.Lock()
	t.proc = p
	t.mu.Unlock()
	go t.respond(p)
	p.start()
	return t, nil
}

// StartProgramT is StartProgram for a test: it registers Close with
// t.Cleanup, fails the test if the program cannot start, and keeps the same
// debug log as StartT.
func StartProgramT(tb testing.TB, prog Program, opts ...Option) *Terminal {
	tb.Helper()
	opts = append([]Option{testDefaults(tb)}, opts...)
	term, err := StartProgram(prog, opts...)
	if err != nil {
		tb.Fatalf("tuitest: start program: %v", err)
	}
	tb.Cleanup(func() { term.cleanup(tb) })
	return term
}

// inProcess adapts a Program to process. Input goes through a pipe, so a
// write blocks until the program reads it, as a write to a full PTY does, and
// the write timeout covers a program that stops reading.
type inProcess struct {
	prog   Program
	cols   int
	rows   int
	grace  time.Duration
	inR    *io.PipeReader
	inW    *io.PipeWriter
	onData func([]byte)
	onExit func(int)

	// outMu orders the program's writes and its exit: once Run has returned
	// and onExit has run, no more output reaches the Terminal.
	outMu sync.Mutex
	done  chan struct{}

	mu     sync.Mutex
	exited bool
	code   int

	closeOnce sync.Once
	closeErr  error
}

func newInProcess(prog Program, onData func([]byte), onExit func(int), cols, rows int, grace time.Duration) *inProcess {
	switch {
	case grace == 0:
		grace = ptyproc.DefaultKillGrace
	case grace < 0:
		grace = 0
	}
	r, w := io.Pipe()
	return &inProcess{
		prog: prog, cols: cols, rows: rows, grace: grace,
		inR: r, inW: w, onData: onData, onExit: onExit,
		done: make(chan struct{}), code: -1,
	}
}

// start runs the program on its own goroutine.
func (p *inProcess) start() {
	go func() {
		code := p.run()
		_ = p.inR.Close()
		p.outMu.Lock()
		p.mu.Lock()
		p.exited, p.code = true, code
		p.mu.Unlock()
		p.onExit(code)
		close(p.done)
		p.outMu.Unlock()
	}()
}

// panicExitCode is the code a Go program exits with when it panics.
const panicExitCode = 2

// run calls Run and turns a panic into what a crashed Go program shows on a
// terminal: the panic and its stack, then exit code 2.
func (p *inProcess) run() (code int) {
	out := programOutput{p}
	defer func() {
		if v := recover(); v != nil {
			fmt.Fprintf(out, "\r\npanic: %v\r\n\r\n%s", v, crlf(debug.Stack()))
			code = panicExitCode
		}
	}()
	return p.prog.Run(p.inR, out, p.cols, p.rows)
}

// crlf turns each "\n" into "\r\n", the way a PTY shows a program's text.
func crlf(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/32)
	for _, c := range b {
		if c == '\n' {
			out = append(out, '\r')
		}
		out = append(out, c)
	}
	return out
}

// programOutput is the writer the program draws with.
type programOutput struct{ p *inProcess }

func (w programOutput) Write(b []byte) (int, error) {
	w.p.outMu.Lock()
	defer w.p.outMu.Unlock()
	select {
	case <-w.p.done:
		// Output after the exit is dropped, as nothing reads a closed PTY.
		return 0, io.ErrClosedPipe
	default:
	}
	// The Terminal copies what it keeps, so the program may reuse b.
	w.p.onData(b)
	return len(b), nil
}

func (p *inProcess) Write(b []byte) error {
	_, err := p.inW.Write(b)
	return err
}

func (p *inProcess) Resize(cols, rows int) error {
	select {
	case <-p.done:
		return io.ErrClosedPipe
	default:
	}
	p.prog.Resize(cols, rows)
	return nil
}

func (p *inProcess) Done() <-chan struct{} { return p.done }

func (p *inProcess) ExitCode() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, p.exited
}

func (p *inProcess) ExitStatus() (ptyproc.Status, bool) {
	code, exited := p.ExitCode()
	return ptyproc.Status{Code: code}, exited
}

// Pid is 0: the program is not a process of its own.
func (p *inProcess) Pid() int { return 0 }

// Close asks the program to stop and waits up to the kill grace for Run to
// return. A program that does not return is reported, since its goroutine
// keeps running for the rest of the test binary.
func (p *inProcess) Close() error {
	p.closeOnce.Do(func() {
		p.prog.Stop()
		// Closing the input as well ends a program blocked on a read.
		_ = p.inW.Close()
		timer := time.NewTimer(p.grace)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			p.closeErr = fmt.Errorf("the program did not return from Run within %s of Stop", p.grace)
		}
	})
	return p.closeErr
}
