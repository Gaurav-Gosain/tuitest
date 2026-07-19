//go:build !windows

package ptyproc

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

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
