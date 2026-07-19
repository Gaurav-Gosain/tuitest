// Package ptyproc owns the process and PTY lifecycle for tuitest: spawning a
// child attached to a pseudo-terminal, pumping its output, resizing, EOF and
// exit-code handling, and process-tree teardown. It is deliberately separate
// from the screen-matching logic so the two compose independently (the
// go-expect separation of console ownership from expectation matching).
package ptyproc

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/xpty"
)

// Config configures a spawn.
type Config struct {
	Argv []string // argv[0] is the program, argv[1:] the arguments
	Env  []string // full environment ("KEY=VALUE" entries)
	Dir  string   // working directory, empty for inherit
	Cols int
	Rows int
}

// Handler receives lifecycle callbacks from the pump goroutine. Callbacks are
// invoked from a single dedicated goroutine, never concurrently with each
// other, so the receiver only needs to guard state it also touches elsewhere.
type Handler struct {
	// OnData is called with each chunk read from the PTY master. The slice is
	// owned by the callback for the duration of the call only.
	OnData func([]byte)
	// OnClose is called once, after the child has exited and been reaped, with
	// its exit code (-1 if unknown).
	OnClose func(code int)
}

// Status describes how a child finished. Code is the exit status, or -1 when
// the child died from a signal or the status could not be read. Signaled and
// Signal separate a real crash (SIGSEGV, SIGABRT, SIGBUS) from an ordinary
// non-zero exit, which ExitCode alone flattens to -1 and loses.
type Status struct {
	Code     int
	Signaled bool
	Signal   syscall.Signal
}

// Process is a spawned child attached to a PTY.
type Process struct {
	pty xpty.Pty
	cmd *exec.Cmd

	writeMu sync.Mutex // serializes writes to the PTY master

	mu     sync.Mutex
	exited bool
	status Status

	done      chan struct{} // closed once the child is reaped and OnClose has run
	reapOnce  sync.Once
	closeOnce sync.Once
	closeErr  error // teardown result, published by closeOnce
}

// Start spawns the configured child in a new PTY and starts pumping output.
func Start(cfg Config, h Handler) (*Process, error) {
	if len(cfg.Argv) == 0 {
		return nil, errors.New("ptyproc: empty argv")
	}
	if cfg.Cols <= 0 {
		cfg.Cols = 80
	}
	if cfg.Rows <= 0 {
		cfg.Rows = 24
	}

	pty, err := xpty.NewPty(cfg.Cols, cfg.Rows)
	if err != nil {
		return nil, err
	}

	// Take the line discipline out of the way before the child exists, so that
	// every byte written to this PTY reaches the program unaltered from the very
	// first one. See neutraliseLineDiscipline for why this cannot wait.
	if err := neutraliseLineDiscipline(pty); err != nil {
		_ = pty.Close()
		return nil, err
	}

	cmd := exec.Command(cfg.Argv[0], cfg.Argv[1:]...) //nolint:gosec
	cmd.Env = cfg.Env
	cmd.Dir = cfg.Dir
	setSysProcAttr(cmd)

	if err := pty.Start(cmd); err != nil {
		_ = pty.Close()
		return nil, err
	}

	// The child now holds its own descriptors for the slave end. Close the
	// parent's copy so that when the child exits, a read on the master returns
	// EOF/EIO and the pump goroutine can reap it. Without this the parent keeps
	// the slave open and the master read blocks forever.
	if u, ok := pty.(*xpty.UnixPty); ok {
		_ = u.Slave().Close()
	}

	p := &Process{
		pty:    pty,
		cmd:    cmd,
		status: Status{Code: -1},
		done:   make(chan struct{}),
	}
	go p.pump(h)
	return p, nil
}

func (p *Process) pump(h Handler) {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.pty.Read(buf)
		if n > 0 && h.OnData != nil {
			h.OnData(buf[:n])
		}
		if err != nil {
			break
		}
	}
	st := p.reap()
	if h.OnClose != nil {
		h.OnClose(st.Code)
	}
	// Close done only after the handler has recorded the exit. A waiter woken
	// by Done must not be able to observe a receiver that has not yet been told
	// the child is gone.
	close(p.done)
}

// reap waits for the child exactly once and records how it finished.
func (p *Process) reap() Status {
	p.reapOnce.Do(func() {
		_ = p.cmd.Wait()
		st := Status{Code: -1}
		if p.cmd.ProcessState != nil {
			st.Code = p.cmd.ProcessState.ExitCode()
			st.Signaled, st.Signal = waitSignal(p.cmd.ProcessState)
		}
		p.mu.Lock()
		p.exited = true
		p.status = st
		p.mu.Unlock()
	})
	p.mu.Lock()
	st := p.status
	p.mu.Unlock()
	return st
}

// Write sends input bytes to the child.
// Write sends bytes to the child. Two goroutines write here: the caller
// sending keystrokes, and the output pump forwarding emulator query responses.
// The lock keeps a short write from being interleaved into the middle of an
// escape sequence from the other writer.
func (p *Process) Write(b []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.pty.Write(b)
	return err
}

// Resize changes the PTY window size; the kernel delivers SIGWINCH to the child.
func (p *Process) Resize(cols, rows int) error {
	return p.pty.Resize(cols, rows)
}

// ExitCode reports the child's exit code and whether it has exited.
func (p *Process) ExitCode() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status.Code, p.exited
}

// ExitStatus reports how the child finished, including signal death, and
// whether it has exited at all.
func (p *Process) ExitStatus() (Status, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.exited
}

// Pid returns the child's process id, or 0 if it never started.
func (p *Process) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Done returns a channel closed once the child has been reaped and the
// Handler's OnClose callback has returned.
func (p *Process) Done() <-chan struct{} { return p.done }

// Close tears down the whole process tree and closes the PTY. It is idempotent
// and safe to call from a cleanup hook even after the child exited.
//
// It returns a non-nil error when something the child spawned was still running
// after SIGKILL. That is a leak the caller needs to know about: a test that
// ignores it hands the next test a machine with stray processes on it, which is
// how a suite ends up flooding a workstation.
func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		exited := p.exited
		pid := 0
		if p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		p.mu.Unlock()

		// Only signal while the child is unreaped. Once it has been waited for,
		// the kernel may have recycled its pid, and signalling a recycled pid
		// would tear down an unrelated process group. Descendants of an
		// already-exited child are therefore left alone; they are orphans the
		// harness can no longer identify safely.
		if !exited && pid > 0 {
			p.closeErr = terminateGroup(pid, p.done)
		}
		// Close the PTY only after the process tree is gone, so the pump
		// goroutine is not left spinning on a half-open descriptor.
		_ = p.pty.Close()
	})
	return p.closeErr
}

// Probe reports whether a pseudo-terminal can be allocated at all, by opening
// one and immediately closing it. It spawns no child, so it is safe to call
// from diagnostics without risking a stray process. A non-nil error is the
// reason allocation failed, which on a container without /dev/pts is exactly
// what a user needs to see.
func Probe(cols, rows int) error {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	pty, err := xpty.NewPty(cols, rows)
	if err != nil {
		return err
	}
	return pty.Close()
}
