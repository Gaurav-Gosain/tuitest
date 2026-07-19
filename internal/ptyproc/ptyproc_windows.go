//go:build windows

package ptyproc

import (
	"os"
	"os/exec"
	"syscall"
)

// Windows is out of scope for the first version. These stubs keep the package
// compiling; process-group teardown falls back to closing the ConPTY.
func setSysProcAttr(*exec.Cmd) {}

func terminateGroup(int, <-chan struct{}) {}

// waitSignal has no meaning on Windows, which has no signal-death exit status;
// a crash there surfaces as an ordinary non-zero exit code instead.
func waitSignal(*os.ProcessState) (bool, syscall.Signal) { return false, 0 }
