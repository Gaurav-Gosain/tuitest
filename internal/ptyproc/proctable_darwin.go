//go:build darwin

package ptyproc

import "golang.org/x/sys/unix"

// sZomb is SZOMB from <sys/proc.h>: the process has exited and is waiting to be
// reaped.
const sZomb = 5

// procTableNative reads the process table from the kernel with the
// kern.proc.all sysctl, which is what ps itself reads on macOS. Doing it
// directly saves a fork and exec of ps on every teardown, which cost around
// 15ms each, and keeps teardown working when ps is not on PATH or the process
// is out of file descriptors.
func procTableNative() map[int]procInfo {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil || len(procs) == 0 {
		return nil
	}
	table := make(map[int]procInfo, len(procs))
	for i := range procs {
		p := &procs[i]
		table[int(p.Proc.P_pid)] = procInfo{
			ppid:   int(p.Eproc.Ppid),
			pgrp:   int(p.Eproc.Pgid),
			zombie: p.Proc.P_stat == sZomb,
		}
	}
	return table
}

// processZombie reports whether pid is a zombie, and whether the answer is
// known at all. Without it macOS has only the signal probe, which counts a
// zombie as alive.
func processZombie(pid int) (zombie, known bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return false, false
	}
	return kp.Proc.P_stat == sZomb, true
}
