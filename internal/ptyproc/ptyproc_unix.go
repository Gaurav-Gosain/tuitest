//go:build !windows

package ptyproc

import (
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

// terminateGroup signals the process group led by pid: SIGTERM first, then
// SIGKILL if the group does not exit within a short grace period. It returns
// as soon as the child is reaped (done closed) or the grace elapses.
func terminateGroup(pid int, done <-chan struct{}) {
	const grace = 2 * time.Second

	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-done:
	case <-time.After(grace):
	}
}
