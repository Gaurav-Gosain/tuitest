package tuitest_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// processAlive reports whether pid is a running process. A zombie answers yes
// here, which is why the callers below only use it on processes they never
// parented and therefore never see as zombies.
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// Close must reap descendants that left the child's process group. A program
// that calls setsid (what daemonizing looks like, and what tuios's daemon does)
// never receives a signal aimed at the group, so a teardown that only signals
// the group leaves it running after every test that spawned it. This is the
// leak that fills a workstation with stray processes.
//
// Verified to fail: restoring the group-only teardown (kill(-pid) alone,
// without the descendant snapshot) leaves the grandchild running and this test
// reports the leak.
func TestCloseReapsDescendantsThatLeftTheProcessGroup(t *testing.T) {
	sh := shellPath(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	// The child spawns a grandchild in its own session, so the grandchild is
	// outside the process group teardown signals.
	script := `setsid ` + sh + ` -c 'echo $$ > ` + pidFile + `; exec sleep 30' </dev/null >/dev/null 2>&1 &
echo SPAWNED
sleep 30`
	term := tuitest.StartT(t, []string{sh, "-c", script})

	if err := term.WaitForText("SPAWNED", 10*time.Second); err != nil {
		t.Fatalf("child never started the grandchild: %v", err)
	}

	pid := waitForPidFile(t, pidFile, 10*time.Second)
	// Never leave the grandchild behind, whatever this test concludes.
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	if !processAlive(pid) {
		t.Fatalf("grandchild %d was not running before Close", pid)
	}

	if err := term.Close(); err != nil {
		t.Errorf("Close reported a teardown failure: %v", err)
	}

	// Teardown polls the process table, so the grandchild is gone by the time
	// Close returns; allow a short margin rather than asserting instantly.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("grandchild %d survived Close: teardown is not transitive", pid)
	}
}

// waitForPidFile reads a pid a spawned process wrote, tolerating the window
// between the file being created and the write landing.
func waitForPidFile(t *testing.T, path string, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid appeared in %s within %s", path, within)
	return 0
}

// A program can hide text with SGR 8, and a real terminal draws those cells
// blank. The harness must not report the hidden runes, or a wait succeeds
// against text that is not on screen.
//
// Verified to fail: dropping the Conceal branch from screenSnapshot.Line makes
// both assertions below fire, because "hunter2" is reported as visible and
// WaitForText matches it.
func TestConcealedTextIsNotReportedAsOnScreen(t *testing.T) {
	sh := shellPath(t)
	term := tuitest.StartT(t, []string{
		sh, "-c", `printf 'VISIBLE \033[8mhunter2\033[0m DONE\n'; sleep 10`,
	})
	if err := term.WaitForText("DONE", 10*time.Second); err != nil {
		t.Fatalf("program never painted: %v", err)
	}

	if snap := term.Snapshot(); strings.Contains(snap, "hunter2") {
		t.Errorf("concealed text is reported on screen: %q", snap)
	}
	if err := term.WaitForText("hunter2", 300*time.Millisecond); err == nil {
		t.Error("WaitForText matched concealed text that no user can see")
	}
	// The styled encoding must still record that something was concealed there,
	// so a golden can see the difference.
	if styled := term.SnapshotStyled(); !strings.Contains(styled, " c") {
		t.Errorf("styled snapshot does not record the concealed run:\n%s", styled)
	}
}

// shellPath locates a POSIX shell, skipping the test where there is none.
func shellPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/bin/sh", "/usr/bin/sh"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no POSIX shell available")
	return ""
}
