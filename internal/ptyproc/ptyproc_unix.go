//go:build !windows

package ptyproc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// terminateSurvivors tears down whatever is left of the process group of a child
// that has already been waited for.
//
// Once the child is reaped its descendants can no longer be found by walking
// parent links: the kernel reparented them to init the moment it died, and an
// orphan reparented to init is indistinguishable from an unrelated process. The
// process group is what survives that. It stays this child's group for as long
// as it has a member (see groupExists), so signalling it is safe even though the
// leader is gone, and a program that backgrounded something and then exited is
// torn down instead of being left running with Close reporting success.
//
// A descendant that called setsid before the child exited is still lost: it left
// the group, its parent link died with the child, and nothing the harness can
// observe ties it back here. Close names what it can and stays quiet about what
// it genuinely cannot see.
func terminateSurvivors(pgid int) error {
	const grace = 2 * time.Second

	// The leader has been reaped, so its pid was released back to the kernel.
	// If something is answering to that number now it is an unrelated process
	// that happens to have been handed it, and on a machine that has wrapped
	// through the pid space that process is quite likely another child of this
	// harness. Signalling its group would tear down a live test. The group id is
	// only safe to use while nothing has taken the number back.
	if processLive(pgid) {
		return nil
	}
	if !groupExists(pgid) {
		return nil // the common case: the child took its whole tree with it
	}

	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	awaitGroupGone(pgid, grace)

	if left := liveProcs(groupMembers(pgid)); len(left) > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		awaitGroupGone(pgid, grace)
	}

	if left := liveProcs(groupMembers(pgid)); len(left) > 0 {
		return fmt.Errorf("ptyproc: %d process(es) survived teardown: %v", len(left), left)
	}
	return nil
}

// awaitGroupGone polls until nothing living is left in the process group or
// grace expires.
//
// The cheap group probe cannot end the wait on its own, because it answers yes
// for a group holding nothing but zombies, and a zombie descendant is reaped by
// init on its own schedule. Falling back to the process table keeps a routine
// teardown from stalling for the full grace period on a corpse.
func awaitGroupGone(pgid int, grace time.Duration) {
	const tick = 20 * time.Millisecond
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !groupExists(pgid) {
			return
		}
		if len(liveProcs(groupMembers(pgid))) == 0 {
			return
		}
		time.Sleep(tick)
	}
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

// SignalName returns the symbolic name of sig, such as "SIGKILL", or "signal N"
// for a number the platform does not name. syscall.Signal's own String is the C
// library's description ("killed", "terminated"), which reads badly after
// "killed by" and is not what a reader searches for.
func SignalName(sig syscall.Signal) string {
	if name := unix.SignalName(sig); name != "" {
		return name
	}
	return "signal " + strconv.Itoa(int(sig))
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
