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

// Process is a spawned child attached to a PTY.
type Process struct {
	pty xpty.Pty
	cmd *exec.Cmd

	mu       sync.Mutex
	exited   bool
	exitCode int

	done      chan struct{} // closed once the child is reaped and OnClose has run
	reapOnce  sync.Once
	closeOnce sync.Once
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
		pty:      pty,
		cmd:      cmd,
		exitCode: -1,
		done:     make(chan struct{}),
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
	code := p.reap()
	if h.OnClose != nil {
		h.OnClose(code)
	}
	// Close done only after the handler has recorded the exit. A waiter woken
	// by Done must not be able to observe a receiver that has not yet been told
	// the child is gone.
	close(p.done)
}

// reap waits for the child exactly once and records its exit code.
func (p *Process) reap() int {
	p.reapOnce.Do(func() {
		_ = p.cmd.Wait()
		code := -1
		if p.cmd.ProcessState != nil {
			code = p.cmd.ProcessState.ExitCode()
		}
		p.mu.Lock()
		p.exited = true
		p.exitCode = code
		p.mu.Unlock()
	})
	p.mu.Lock()
	code := p.exitCode
	p.mu.Unlock()
	return code
}

// Write sends input bytes to the child.
func (p *Process) Write(b []byte) error {
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
	return p.exitCode, p.exited
}

// Done returns a channel closed once the child has been reaped and the
// Handler's OnClose callback has returned.
func (p *Process) Done() <-chan struct{} { return p.done }

// Close tears down the whole process group and closes the PTY. It is
// idempotent and safe to call from a cleanup hook even after the child exited.
func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		exited := p.exited
		pid := 0
		if p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		p.mu.Unlock()

		if !exited && pid > 0 {
			terminateGroup(pid, p.done)
		}
		// Close the PTY only after the process group is gone, so the pump
		// goroutine is not left spinning on a half-open descriptor.
		_ = p.pty.Close()
	})
	return nil
}
