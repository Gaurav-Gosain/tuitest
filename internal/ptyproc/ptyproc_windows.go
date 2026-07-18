//go:build windows

package ptyproc

import "os/exec"

// Windows is out of scope for the first version. These stubs keep the package
// compiling; process-group teardown falls back to closing the ConPTY.
func setSysProcAttr(*exec.Cmd) {}

func terminateGroup(int, <-chan struct{}) {}
