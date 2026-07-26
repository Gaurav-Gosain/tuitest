//go:build !windows

package ptyproc

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestProcTablePSAgreesWithProc guards the ps fallback, which is what runs on
// macOS and is therefore never exercised by CI on Linux. It is the column layout
// that is fragile: the fields are positional, so adding one to the ps invocation
// and forgetting to shift the indexes below reads the parent as the group and
// teardown then signals the wrong processes. Comparing it against /proc, where
// both are available, is the only cheap way to keep the two readings honest.
func TestProcTablePSAgreesWithProc(t *testing.T) {
	if !hasProcFS() {
		t.Skip("no /proc to compare against")
	}
	fromProc := procTableProc()
	if len(fromProc) == 0 {
		t.Skip("could not read /proc")
	}
	if _, err := exec.Command("ps", "-Ao", "pid=").Output(); err != nil {
		t.Skipf("ps is unavailable: %v", err)
	}
	// Past this point ps works, so an empty table is a parse failure and not an
	// absence. Skipping on it would hide exactly the breakage this test exists
	// to catch, since a shifted column makes every line fail to parse.
	fromPS := procTablePS()
	if len(fromPS) == 0 {
		t.Fatal("procTablePS parsed nothing out of a working ps")
	}

	self := os.Getpid()
	ps, ok := fromPS[self]
	if !ok {
		t.Fatalf("ps did not list this process (%d)", self)
	}
	proc, ok := fromProc[self]
	if !ok {
		t.Fatalf("/proc did not list this process (%d)", self)
	}
	if ps.ppid != proc.ppid {
		t.Errorf("ps reports ppid %d, /proc reports %d", ps.ppid, proc.ppid)
	}
	if ps.pgrp != proc.pgrp {
		t.Errorf("ps reports pgrp %d, /proc reports %d", ps.pgrp, proc.pgrp)
	}
	if want, err := syscall.Getpgid(self); err == nil && proc.pgrp != want {
		t.Errorf("/proc reports pgrp %d, getpgid says %d", proc.pgrp, want)
	}
}

// TestGroupMembersFindsThisProcess pins the reading teardown of an exited child
// depends on: the process group is the only handle left once parent links are
// gone, so groupMembers has to actually find what is in one.
func TestGroupMembersFindsThisProcess(t *testing.T) {
	self := os.Getpid()
	pgid, err := syscall.Getpgid(self)
	if err != nil {
		t.Skipf("getpgid: %v", err)
	}
	if !groupExists(pgid) {
		t.Fatalf("groupExists(%d) is false for a group this process is in", pgid)
	}
	members := groupMembers(pgid)
	if len(members) == 0 {
		t.Skip("the process table could not be read")
	}
	for _, pid := range members {
		if pid == self {
			return
		}
	}
	t.Errorf("groupMembers(%d) = %v, missing this process (%d)", pgid, members, self)
}
