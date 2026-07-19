//go:build !windows

package ptyproc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// procInfo is the part of a process table entry teardown cares about: who the
// parent is, and whether the process is a zombie. A zombie has already died and
// is only waiting to be reaped, so it must not be counted as a survivor.
type procInfo struct {
	ppid   int
	zombie bool
}

// procTable reads the system process table. It returns nil when the table
// cannot be read at all, which degrades teardown to signalling the process
// group alone rather than failing.
func procTable() map[int]procInfo {
	if t := procTableProc(); len(t) > 0 {
		return t
	}
	return procTablePS()
}

// procTableProc reads /proc, which exists on Linux. Each stat line is
// "pid (comm) state ppid ...", and comm is an arbitrary string that may itself
// contain spaces and parentheses, so the fields are taken after the final ')'
// rather than by splitting the whole line.
func procTableProc() map[int]procInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	table := make(map[int]procInfo, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // the process exited while we were looking at it
		}
		s := string(b)
		close := strings.LastIndexByte(s, ')')
		if close < 0 || close+2 >= len(s) {
			continue
		}
		fields := strings.Fields(s[close+1:])
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		table[pid] = procInfo{ppid: ppid, zombie: fields[0] == "Z"}
	}
	return table
}

// procTablePS is the fallback for Unixes without /proc, notably macOS. It shells
// out to ps, which is specified by POSIX and present everywhere tuitest runs.
func procTablePS() map[int]procInfo {
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=,state=").Output()
	if err != nil {
		return nil
	}
	table := map[int]procInfo{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		zombie := len(fields) > 2 && strings.HasPrefix(fields[2], "Z")
		table[pid] = procInfo{ppid: ppid, zombie: zombie}
	}
	return table
}

// descendants returns every process reachable from root through parent links,
// excluding root itself.
//
// This is what makes teardown transitive. Signalling the process group catches
// the child and anything it spawned normally, but a process that called setsid
// (exactly what a daemon does when it detaches, and what tuios's daemon does)
// has left that group and will never see the signal. Its parent link is still
// intact while its ancestors are alive, so the tree must be snapshotted before
// anything is killed: once the ancestors die the orphan is reparented to init
// and there is no longer any way to tell it apart from an unrelated process.
func descendants(root int) []int {
	// Prefer the kernel's own child list. It costs one small read per process
	// in the tree, whereas building the table below walks every process on the
	// machine. That difference is worth having: teardown runs on every spawn,
	// and the overwhelmingly common case is a child with no children at all,
	// which this answers with a single read.
	if tree, ok := descendantsViaChildren(root); ok {
		return tree
	}

	table := procTable()
	if len(table) == 0 {
		return nil
	}
	children := make(map[int][]int, len(table))
	for pid, info := range table {
		if pid != info.ppid { // guard against a self-parenting entry
			children[info.ppid] = append(children[info.ppid], pid)
		}
	}
	var out []int
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if seen[child] {
				continue // a cycle cannot happen, but never loop forever on one
			}
			seen[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

// descendantsViaChildren walks the tree using /proc/<pid>/task/<tid>/children,
// which lists a process's direct children. It reports false when that interface
// is unavailable (a kernel built without it, or a system without /proc), so the
// caller can fall back to scanning the whole process table.
func descendantsViaChildren(root int) ([]int, bool) {
	first, ok := childrenOf(root)
	if !ok {
		return nil, false
	}
	var out []int
	seen := map[int]bool{root: true}
	queue := first
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		// A child that exits between listing and this read simply contributes
		// nothing, which is correct: it is already gone.
		kids, _ := childrenOf(pid)
		queue = append(queue, kids...)
	}
	return out, true
}

// childrenOf returns the direct children of pid. Children are listed per
// thread, so every task of the process is consulted.
func childrenOf(pid int) ([]int, bool) {
	dir := "/proc/" + strconv.Itoa(pid) + "/task"
	tasks, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var out []int
	supported := false
	for _, t := range tasks {
		b, err := os.ReadFile(dir + "/" + t.Name() + "/children")
		if err != nil {
			continue
		}
		supported = true
		for _, f := range strings.Fields(string(b)) {
			if c, err := strconv.Atoi(f); err == nil {
				out = append(out, c)
			}
		}
	}
	return out, supported
}

// liveProcs returns the subset of pids that are still running. A zombie counts
// as gone: it holds no resources beyond its exit status and cannot be killed
// again, so reporting it as a survivor would make teardown cry wolf.
//
// Each pid is checked on its own rather than by scanning the whole process
// table, because this runs in the teardown poll loop and a full scan there
// would cost a directory walk every few milliseconds on every Close.
func liveProcs(pids []int) []int {
	var out []int
	for _, pid := range pids {
		if pid > 0 && processLive(pid) {
			out = append(out, pid)
		}
	}
	return out
}

// hasProcFS reports whether /proc is a Linux-style process filesystem, computed
// once. Where it is, a missing /proc/<pid> is proof the process is gone; where
// it is not, that absence means nothing and the signal probe has to be used.
var hasProcFS = sync.OnceValue(func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
})

// processLive reports whether pid is running and not a zombie.
func processLive(pid int) bool {
	if hasProcFS() {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false // the process is gone
		}
		// State is the first field after the final ')', since the command name
		// in between may contain spaces and parentheses.
		s := string(b)
		if close := strings.LastIndexByte(s, ')'); close >= 0 {
			if fields := strings.Fields(s[close+1:]); len(fields) > 0 {
				return fields[0] != "Z"
			}
		}
		return true
	}
	// Without /proc, ask the kernel. This reports a zombie as alive, so a
	// zombie descendant on such a system can be named as a survivor; that is a
	// false alarm rather than a missed leak, which is the safer direction.
	return syscall.Kill(pid, 0) == nil
}
