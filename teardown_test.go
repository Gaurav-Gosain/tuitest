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
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary to use as setsid: %v", err)
	}

	// The child spawns a grandchild in its own session, so the grandchild is
	// outside the process group teardown signals. The test binary stands in for
	// setsid(1), which util-linux ships and macOS and the BSDs do not; see
	// runSetsidHelper.
	script := `"` + self + `" ` + sh + ` -c 'echo $$ > ` + pidFile + `; exec sleep 30' </dev/null >/dev/null 2>&1 &
echo SPAWNED
sleep 30`
	term := tuitest.StartT(t, []string{sh, "-c", script}, tuitest.WithEnv(setsidHelperEnv+"=1"))

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

// setsidHelperEnv makes the test binary act as setsid(1) instead of running
// tests. See runSetsidHelper.
const setsidHelperEnv = "TUITEST_TEST_SETSID_HELPER"

// runSetsidHelper makes this test binary a portable setsid(1): started with
// setsidHelperEnv set, it moves itself into a new session and execs its
// arguments, which then run outside the caller's process group. The command is
// part of util-linux and absent on macOS and the BSDs, which is where the
// teardown it tests matters most, since those systems have no /proc and find
// descendants through ps.
//
// It runs from init so that it takes over before the testing package parses
// the arguments, which are the helper's command line and not test flags.
func runSetsidHelper() {
	if os.Getenv(setsidHelperEnv) == "" || len(os.Args) < 2 {
		return
	}
	_ = os.Unsetenv(setsidHelperEnv)
	if _, err := syscall.Setsid(); err != nil {
		os.Stderr.WriteString("setsid helper: " + err.Error() + "\n")
		os.Exit(1)
	}
	err := syscall.Exec(os.Args[1], os.Args[1:], os.Environ())
	os.Stderr.WriteString("setsid helper: exec " + os.Args[1] + ": " + err.Error() + "\n")
	os.Exit(1)
}

func init() { runSetsidHelper() }

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

// Close must also tear down what a child left behind after exiting on its own.
// Descendants used to be found by walking parent links from the child, which
// stops working the instant the child is reaped and the kernel reparents its
// children to init, so Close did nothing at all in that case and StartT's
// cleanup reported success over a process that was still running. A program that
// backgrounds a worker and returns is an ordinary shape, not an exotic one, and
// this is the leak that fills a workstation with stray processes.
//
// Verified to fail: restoring the "leave an exited child's descendants alone"
// branch in ptyproc.Close leaves the grandchild running and this test reports
// the leak.
func TestCloseReapsSurvivorsOfAChildThatExitedOnItsOwn(t *testing.T) {
	sh := shellPath(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	// The grandchild ignores the hangup the kernel sends when the session
	// leader exits, which is the only reason a backgrounded process outlives
	// the program that spawned it. The child waits for the trap to be in place
	// before exiting, since racing it would let the hangup do teardown's job.
	script := sh + ` -c 'trap "" HUP; echo $$ > ` + pidFile + `; exec sleep 30' </dev/null >/dev/null 2>&1 &
while [ ! -s ` + pidFile + ` ]; do sleep 0.02; done
printf 'SPAWNED\n'
exit 0`

	term, err := tuitest.Start([]string{sh, "-c", script})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, err := term.WaitExit(10 * time.Second); err != nil {
		_ = term.Close()
		t.Fatalf("the child never exited: %v", err)
	}

	pid := waitForPidFile(t, pidFile, 10*time.Second)
	// Never leave the grandchild behind, whatever this test concludes.
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	if !processAlive(pid) {
		t.Skip("the grandchild did not outlive the hangup, so there is nothing to leak")
	}

	if err := term.Close(); err != nil {
		t.Errorf("Close reported a teardown failure: %v", err)
	}
	if processAlive(pid) {
		t.Errorf("grandchild %d survived Close and Close reported success", pid)
	}
}

// A program's last words have to be on screen once WaitExit returns. If the
// harness reaped the child before draining the PTY, an assertion that some
// string is absent from the screen would pass because the string never arrived
// rather than because the program stopped printing it, and a test that passes
// for the wrong reason reports nothing at all.
//
// The guarantee comes from the pump reaping only once the stream has ended, so
// nothing short of restructuring the pump breaks it. That makes this a shape
// test rather than a mutation test: it is repeated so that a future pump which
// races the drain shows up as an intermittent failure here rather than as an
// intermittent failure in somebody's suite.
func TestFinalOutputIsOnScreenWhenWaitExitReturns(t *testing.T) {
	t.Parallel()
	sh := shellPath(t)

	const runs = 200
	for i := range runs {
		term, err := tuitest.Start([]string{sh, "-c", `printf 'LAST_WORDS\n'`}, tuitest.WithSize(40, 6))
		if err != nil {
			t.Fatalf("run %d: spawn: %v", i, err)
		}
		if _, err := term.WaitExit(10 * time.Second); err != nil {
			_ = term.Close()
			t.Fatalf("run %d: %v", i, err)
		}
		if snap := term.Snapshot(); !strings.Contains(snap, "LAST_WORDS") {
			_ = term.Close()
			t.Fatalf("run %d: the child's final output never reached the screen:\n%s", i, snap)
		}
		if err := term.Close(); err != nil {
			t.Fatalf("run %d: close: %v", i, err)
		}
	}
}

// Output a program prints on its way out under Close must reach the screen too.
// Close signals the child and then waits for the pump to drain, so a program
// with a SIGTERM handler still gets its cleanup message recorded; a teardown
// that closed the PTY as soon as the process table said the child was gone
// would truncate it.
//
// Verified to fail: replacing awaitGone's wait on done with a poll of the
// process table loses the message, because a dying child is a zombie in the
// table well before the pump has read the last of what it wrote.
func TestOutputPrintedDuringTeardownReachesTheScreen(t *testing.T) {
	t.Parallel()
	sh := shellPath(t)

	const runs = 50
	for i := range runs {
		script := `trap 'printf "DYING_WORDS\n"; exit 0' TERM
printf 'READY\n'
while :; do sleep 0.05; done`
		term, err := tuitest.Start([]string{sh, "-c", script}, tuitest.WithSize(40, 6))
		if err != nil {
			t.Fatalf("run %d: spawn: %v", i, err)
		}
		if err := term.WaitForText("READY", 10*time.Second); err != nil {
			_ = term.Close()
			t.Fatalf("run %d: %v", i, err)
		}
		if err := term.Close(); err != nil {
			t.Fatalf("run %d: close: %v", i, err)
		}
		if snap := term.Snapshot(); !strings.Contains(snap, "DYING_WORDS") {
			t.Fatalf("run %d: what the program printed while shutting down was lost:\n%s", i, snap)
		}
	}
}
