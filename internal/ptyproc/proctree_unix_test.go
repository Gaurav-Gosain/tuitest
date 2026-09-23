//go:build !windows

package ptyproc

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
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

// TestNativeProcTableAgreesWithPS keeps the native reader honest where there is
// one. On macOS it replaced ps as the source teardown walks, so a wrong field
// offset would make teardown signal the wrong processes, and nothing else in
// the suite would notice.
func TestNativeProcTableAgreesWithPS(t *testing.T) {
	native := procTableNative()
	if native == nil {
		t.Skip("no native process table on this platform")
	}
	if _, err := exec.Command("ps", "-Ao", "pid=").Output(); err != nil {
		t.Skipf("ps is unavailable: %v", err)
	}
	fromPS := procTablePS()
	if len(fromPS) == 0 {
		t.Fatal("procTablePS parsed nothing out of a working ps")
	}

	self := os.Getpid()
	n, ok := native[self]
	if !ok {
		t.Fatalf("the native table did not list this process (%d)", self)
	}
	ps, ok := fromPS[self]
	if !ok {
		t.Fatalf("ps did not list this process (%d)", self)
	}
	if n.ppid != ps.ppid || n.pgrp != ps.pgrp {
		t.Errorf("native table reports ppid %d pgrp %d, ps reports ppid %d pgrp %d",
			n.ppid, n.pgrp, ps.ppid, ps.pgrp)
	}
	if n.ppid != os.Getppid() {
		t.Errorf("native table reports ppid %d, getppid says %d", n.ppid, os.Getppid())
	}
}

// TestProcessLiveTreatsAZombieAsGone pins what liveProcs means by "live". A
// zombie has already exited, so teardown must not wait on it or name it as a
// survivor. Without /proc the signal probe alone answers yes for a zombie,
// which on macOS made a descendant waiting to be reaped count as a leak.
//
// Verified to fail on macOS: dropping the processZombie check from
// processLive reports the zombie below as live.
func TestProcessLiveTreatsAZombieAsGone(t *testing.T) {
	sh := shellPath(t)
	cmd := exec.Command(sh, "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	// Not waited for until the end, so it stays a zombie in between.
	defer func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for processLive(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if processLive(pid) {
		t.Fatalf("processLive(%d) is still true for a child that exited and was not reaped", pid)
	}
	// The pid must still be taken, or the check above proved nothing.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the child was reaped behind the test's back: %v", err)
	}
}
