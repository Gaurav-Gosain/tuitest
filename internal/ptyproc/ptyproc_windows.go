//go:build windows

package ptyproc

import (
	"os"
	"os/exec"
	"syscall"
)

// tuitest has no Windows implementation. The PTY layer underneath it would work
// through ConPTY, but neither guarantee this package makes on top of that does:
// a child cannot be put in its own session with the PTY as its controlling
// terminal, and there is no process group to signal at teardown, so a
// multiplexer's daemon and its pane processes would survive every test.
//
// Rather than ship stubs that compile and then leak processes at run time, the
// package deliberately fails to build on Windows. The undefined identifier
// below is what produces the message, and is the entire purpose of this file.
//
// Run tuitest on Linux, macOS or another Unix-like OS, or under WSL. Adding a
// real backend means a ConPTY spawn plus a Job Object per child so that
// teardown is transitive, and this file is where that work starts.
func init() {
	_ = tuitest_does_not_support_windows__it_needs_a_unix_pty_and_process_groups
}

// setSysProcAttr and terminateGroup exist only so that the deliberate failure
// above is the single error reported, instead of being buried under "undefined"
// errors for the platform hooks the rest of the package calls.

func setSysProcAttr(*exec.Cmd) {}

func terminateGroup(int, <-chan struct{}) {}

// waitSignal has no meaning on Windows, which has no signal-death exit status;
// a crash there surfaces as an ordinary non-zero exit code instead.
func waitSignal(*os.ProcessState) (bool, syscall.Signal) { return false, 0 }
