//go:build !windows

package ptyproc

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/unix"
)

// neutraliseLineDiscipline stops a freshly created PTY from acting on the input
// written to it, so that the bytes tuitest sends are the bytes the program
// reads.
//
// A new PTY comes up in the kernel's default cooked mode, with ISIG, ECHO and
// IXON all on. In that state the line discipline, not the program under test,
// is what consumes some of the input: a 0x03 becomes SIGINT and kills the child
// outright, a 0x13 stops the output stream through flow control, and every byte
// sent is echoed back down the master, where it arrives looking exactly like
// output the program produced.
//
// A TUI normally hides all of this by calling MakeRaw during startup, which is
// why the problem is invisible most of the time. It is a race, though: input
// that lands before the program has finished its own terminal setup is still
// handled by the line discipline. The same tape then either drives the program
// or kills it, depending on how quickly the child was scheduled, which makes a
// test that sends input early nondeterministic under load. Configuring the PTY
// here, before the child is even started, closes that window: there is no
// instant at which a signal-generating line discipline is attached to a running
// child.
//
// Only the settings that reinterpret or manufacture bytes are cleared. Line
// editing (ICANON), CR/NL input mapping and all output processing are left as
// the kernel set them, because those are what a real terminal does to a program
// that has not gone raw, and a harness that changed them would be testing the
// program against a terminal nobody has.
func neutraliseLineDiscipline(pty xpty.Pty) error {
	u, ok := pty.(*xpty.UnixPty)
	if !ok {
		return nil
	}
	fd := int(u.Slave().Fd())

	t, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return fmt.Errorf("ptyproc: reading pty terminal settings: %w", err)
	}

	// Signal generation is the one that actually destroys a run: a ^C in the
	// input stream kills the child outright instead of being delivered to it.
	// The implementation-defined extensions (^V, ^O) rewrite input in the same
	// way, on a smaller scale.
	t.Lflag &^= unix.ISIG | unix.IEXTEN
	// Echo makes the line discipline write tuitest's own keystrokes back down
	// the master, where they are indistinguishable from output the program
	// produced. That corrupts the screen model and the output-byte counter the
	// hang detector reads.
	t.Lflag &^= unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHONL
	// ^S/^Q flow control stops the output stream until a matching ^Q arrives,
	// so a single stray byte can stall a session for as long as it runs.
	t.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY

	if err := unix.IoctlSetTermios(fd, setTermios, t); err != nil {
		return fmt.Errorf("ptyproc: applying pty terminal settings: %w", err)
	}
	return nil
}

// setSysProcAttr puts the child in its own session (and therefore its own
// process group, pgid == pid) with the PTY as its controlling terminal. The
// new session is what lets teardown signal the entire group and reap orphaned
// grandchildren (tuios's daemon and pane processes) that a bare Process.Kill
// would leak.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}
}

// terminateGroup tears down the child and everything it spawned: SIGTERM
// first, then SIGKILL to whatever is still running after a short grace period.
// It returns an error naming the processes that survived even SIGKILL, so a
// caller can report a leak instead of silently assuming teardown worked.
//
// Signalling the process group is not enough on its own. A descendant that
// called setsid has its own group and never receives -pid, which is how a
// daemonizing program outlives every test that spawned it. The descendant tree
// is therefore snapshotted first, while the parent links still lead back here,
// and each member is signalled individually as well.
func terminateGroup(pid int, done <-chan struct{}) error {
	const grace = 2 * time.Second

	tree := descendants(pid)

	signalTree(pid, tree, syscall.SIGTERM)
	awaitGone(pid, tree, done, grace)

	if left := liveProcs(append([]int{pid}, tree...)); len(left) > 0 {
		signalTree(pid, tree, syscall.SIGKILL)
		awaitGone(pid, tree, done, grace)
	}

	if left := liveProcs(append([]int{pid}, tree...)); len(left) > 0 {
		return fmt.Errorf("ptyproc: %d process(es) survived teardown: %v", len(left), left)
	}
	return nil
}

// signalTree sends sig to the child's process group and to every descendant
// that escaped it.
func signalTree(pid int, tree []int, sig syscall.Signal) {
	_ = syscall.Kill(-pid, sig)
	for _, d := range tree {
		if d != pid {
			_ = syscall.Kill(d, sig)
		}
	}
}

// awaitGone waits for the child to be reaped and for any descendants that
// outlived it to go away, bounded by grace.
//
// The wait on done comes first and is not shortcut by the process table. A
// dying child becomes a zombie immediately, so the table would report it gone
// while the pump is still draining the last of its output and has not yet
// recorded the exit code. Returning at that point would let Close race the
// final screen state, and a caller that snapshots after Close would see a
// screen missing whatever the program printed on its way out.
func awaitGone(pid int, tree []int, done <-chan struct{}, grace time.Duration) {
	const tick = 10 * time.Millisecond
	deadline := time.Now().Add(grace)

	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
		return // the child is still running; the caller escalates
	}

	// Descendants are not this process's children, so there is nothing to wait
	// on for them and they have to be polled.
	for time.Now().Before(deadline) {
		if len(liveProcs(tree)) == 0 {
			return
		}
		time.Sleep(tick)
	}
}

// waitSignal reports whether the child was killed by a signal, and which one.
// A signal death is how a real crash (SIGSEGV, SIGABRT, SIGBUS) surfaces;
// os.ProcessState.ExitCode flattens all of them to -1.
func waitSignal(st *os.ProcessState) (bool, syscall.Signal) {
	ws, ok := st.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return false, 0
	}
	return true, ws.Signal()
}
