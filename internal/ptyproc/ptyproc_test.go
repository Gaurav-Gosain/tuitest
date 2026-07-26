package ptyproc

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/xpty"
)

// windowsMarker is the identifier ptyproc_windows.go leaves undefined so that a
// Windows build fails with a message naming the reason.
const windowsMarker = "tuitest_does_not_support_windows"

// TestWindowsBuildFailsLoudly compiles this package for Windows and requires
// that it does not build. The previous stubs compiled and then silently skipped
// process-group teardown, which looks like support until a test leaks a daemon.
// The build has to keep failing, and the failure has to name the reason.
func TestWindowsBuildFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiling for another GOOS is slow")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	// go build without -o writes no artifact, only cache entries.
	cmd := exec.Command("go", "build", "github.com/Gaurav-Gosain/tuitest/internal/ptyproc")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")
	out, err := cmd.CombinedOutput()

	switch {
	case err == nil:
		t.Fatal("the package built for Windows; it must fail loudly instead of shipping stubs")
	case strings.Contains(string(out), windowsMarker):
		// The deliberate failure, naming itself.
	default:
		t.Skipf("Windows build failed for an unrelated reason (no module cache?):\n%s", out)
	}
}

// TestDoneClosesAfterOnClose pins the ordering the exit-code path depends on:
// a waiter woken by Done must never observe a receiver that has not yet been
// handed the exit code. Closing Done first made tuitest's WaitExit report -1
// for a clean exit whenever it won that race.
func TestDoneClosesAfterOnClose(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}

	var handled atomic.Bool
	p, err := Start(Config{Argv: []string{sh, "-c", "exit 3"}, Cols: 20, Rows: 5}, Handler{
		OnClose: func(int) {
			// A real handler is fast; the sleep only widens the window so the
			// ordering is observable rather than probabilistic.
			time.Sleep(50 * time.Millisecond)
			handled.Store(true)
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("child never finished")
	}
	if !handled.Load() {
		t.Fatal("Done was closed before OnClose had returned")
	}
	if code, exited := p.ExitCode(); !exited || code != 3 {
		t.Fatalf("ExitCode() = (%d, %v), want (3, true)", code, exited)
	}
}

// TestControlCharactersReachTheChild pins the property that makes a generated
// reproduction trustworthy: input written to the PTY is delivered to the
// program, not consumed by the line discipline on its way there.
//
// A new PTY starts in cooked mode, so before this was fixed a 0x03 sent
// straight after the spawn raced the program's own MakeRaw. Winning the race
// meant the byte was read; losing it meant SIGINT killed the child and the
// harness graded a program that had never run. The fuzzer minimises exactly to
// that shape, and the result was corpus entries that reproduced roughly one
// time in five.
//
// The shell here is deliberately not a TUI and never goes raw, so the only
// thing that can be keeping the signal from firing is the PTY configuration.
func TestControlCharactersReachTheChild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	for _, tool := range []string{"dd", "od", "tr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}

	// Reads one byte and reports its value, so the test can tell "delivered"
	// from "the shell exited for some other reason".
	script := `dd bs=1 count=1 2>/dev/null | od -An -tu1 | tr -d ' \n'; echo`

	var out strings.Builder
	var mu sync.Mutex
	p, err := Start(Config{Argv: []string{sh, "-c", script}, Cols: 20, Rows: 5}, Handler{
		OnData: func(b []byte) {
			mu.Lock()
			out.Write(b)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	// Written immediately, while the child is still starting: this is the
	// window the bug lived in. The newline is only there to complete the line
	// for the still-canonical read; the 0x03 in front of it is the subject.
	if err := p.Write([]byte{3, '\n'}); err != nil {
		t.Fatalf("writing to the pty: %v", err)
	}

	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the child never exited")
	}

	if st, _ := p.ExitStatus(); st.Signaled {
		t.Fatalf("the child was killed by %v: the line discipline turned input into a signal "+
			"instead of delivering it", st.Signal)
	}
	mu.Lock()
	got := out.String()
	mu.Unlock()
	if strings.TrimSpace(got) != "3" {
		t.Fatalf("the child read %q, want the 0x03 that was sent to it", strings.TrimSpace(got))
	}
}

// shellPath locates a POSIX shell, skipping the test where there is none.
func shellPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	return sh
}

// TestTrailingOutputReachesTheHandlerBeforeClose pins the ordering an assertion
// on a program's last words depends on: everything the child wrote must have
// been handed to OnData before OnClose reports the exit. A pump that reaped on
// the first sign of the child's death instead of on end-of-stream would drop the
// tail, and the failure that produces is silent in the worst way: a test
// asserting that some string is absent from the screen passes because the string
// never arrived, not because the program stopped printing it.
//
// The window is narrow enough that a single spawn proves nothing, so the run is
// repeated; the loop is what gives this test its teeth.
func TestTrailingOutputReachesTheHandlerBeforeClose(t *testing.T) {
	sh := shellPath(t)

	const runs = 200
	for i := range runs {
		var mu sync.Mutex
		var seen bytes.Buffer
		var atClose string

		p, err := Start(Config{Argv: []string{sh, "-c", `printf 'LAST_WORDS\n'`}, Cols: 40, Rows: 6}, Handler{
			OnData: func(b []byte) {
				mu.Lock()
				seen.Write(b)
				mu.Unlock()
			},
			OnClose: func(int) {
				mu.Lock()
				atClose = seen.String()
				mu.Unlock()
			},
		})
		if err != nil {
			t.Fatalf("run %d: start: %v", i, err)
		}

		select {
		case <-p.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("run %d: child never finished", i)
		}
		mu.Lock()
		got := atClose
		mu.Unlock()
		if !strings.Contains(got, "LAST_WORDS") {
			t.Fatalf("run %d: OnClose ran before the child's final output was delivered: %q", i, got)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("run %d: close: %v", i, err)
		}
	}
}

// TestExitCodeIsMinusOneForSignalDeath states the exit-code contract in a test
// so that it cannot drift silently.
//
// Code is what os.ProcessState reports, which is -1 for every signal death, and
// deliberately not the shell's 128+signal: a harness that reported 137 for
// SIGKILL could not tell that apart from a program that exited 137 on its own.
// The cost is that -1 alone is ambiguous, which is what ExitStatus exists to
// resolve, and this test pins both halves of that bargain.
func TestExitCodeIsMinusOneForSignalDeath(t *testing.T) {
	sh := shellPath(t)

	cases := []struct {
		name     string
		script   string
		wantCode int
		wantSig  syscall.Signal
	}{
		{"clean exit", "exit 0", 0, 0},
		{"non-zero exit", "exit 7", 7, 0},
		{"SIGTERM", "kill -TERM $$", -1, syscall.SIGTERM},
		{"SIGKILL", "kill -KILL $$", -1, syscall.SIGKILL},
		{"SIGSEGV", "kill -SEGV $$", -1, syscall.SIGSEGV},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Start(Config{Argv: []string{sh, "-c", tc.script}, Cols: 20, Rows: 5}, Handler{})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = p.Close() })

			select {
			case <-p.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("child never finished")
			}

			code, exited := p.ExitCode()
			if !exited {
				t.Fatal("ExitCode reports the child has not exited")
			}
			if code != tc.wantCode {
				t.Errorf("ExitCode() = %d, want %d", code, tc.wantCode)
			}
			// os/exec is the reference: this package must not invent a
			// different number for the same death.
			ref := exec.Command(sh, "-c", tc.script)
			_ = ref.Run()
			if refCode := ref.ProcessState.ExitCode(); refCode != code {
				t.Errorf("ExitCode() = %d but os/exec reports %d for the same death", code, refCode)
			}

			st, _ := p.ExitStatus()
			if tc.wantSig == 0 {
				if st.Signaled {
					t.Errorf("ExitStatus reports signal death for %q: %+v", tc.script, st)
				}
				return
			}
			if !st.Signaled || st.Signal != tc.wantSig {
				t.Fatalf("ExitStatus() = %+v, want a %v death", st, tc.wantSig)
			}
			// The shell would have reported 128+signal here. Saying so in the
			// test is the point: the difference is a decision, not an oversight.
			if shellWould := 128 + int(tc.wantSig); code == shellWould {
				t.Errorf("ExitCode() adopted the shell's %d convention; -1 plus ExitStatus is the contract", shellWould)
			}
		})
	}
}

// TestCloseTearsDownSurvivorsOfAnExitedChild covers the leak that opens once the
// child is gone. Teardown used to walk parent links to find descendants, which
// stops working the moment the child is reaped and its children are reparented
// to init, so Close did nothing at all for an already-exited child and reported
// success while a process it had spawned kept running.
//
// The grandchild here ignores the hangup the kernel sends when the session
// leader exits, which is the only reason a backgrounded process outlives its
// parent in the first place, and is exactly what a program that means to
// daemonize does.
//
// Verified to fail: restoring the "leave an exited child's descendants alone"
// branch in Close leaves the grandchild running and this test reports the leak.
func TestCloseTearsDownSurvivorsOfAnExitedChild(t *testing.T) {
	sh := shellPath(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	// The child waits for the grandchild to have installed its hangup trap
	// before exiting. Racing it would let the kernel's hangup reach the
	// grandchild first and kill it, which looks like teardown working.
	script := sh + ` -c 'trap "" HUP; echo $$ > ` + pidFile + `; exec sleep 30' </dev/null >/dev/null 2>&1 &
while [ ! -s ` + pidFile + ` ]; do sleep 0.02; done
printf 'SPAWNED\n'
exit 0`

	var mu sync.Mutex
	var out bytes.Buffer
	p, err := Start(Config{Argv: []string{sh, "-c", script}, Cols: 40, Rows: 6}, Handler{
		OnData: func(b []byte) { mu.Lock(); out.Write(b); mu.Unlock() },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		_ = p.Close()
		t.Fatal("child never finished")
	}
	mu.Lock()
	spawned := strings.Contains(out.String(), "SPAWNED")
	mu.Unlock()
	if !spawned {
		_ = p.Close()
		t.Fatal("the child never reported spawning the grandchild")
	}

	pid := waitForPid(t, pidFile, 10*time.Second)
	// Never leave the grandchild behind, whatever this test concludes.
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	if !processLive(pid) {
		t.Skip("the grandchild did not outlive the hangup, so there is nothing to leak")
	}

	if err := p.Close(); err != nil {
		t.Errorf("Close reported a teardown failure: %v", err)
	}
	if processLive(pid) {
		t.Errorf("grandchild %d survived Close of an already-exited child", pid)
	}
}

// waitForPid reads a pid a spawned process wrote, tolerating the window between
// the file being created and the write landing.
func waitForPid(t *testing.T, path string, within time.Duration) int {
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

// TestCloseReleasesTheMasterAndReportsIt covers the descriptor half of teardown.
// Start closes the parent's copy of the slave so that the master sees EOF, and
// xpty's Close then closes that same slave a second time and hands back the
// resulting "file already closed" whenever the master closed cleanly. Reporting
// that would fail every test, so it used to be discarded wholesale, and with it
// any real failure to release the master. Close now closes the master itself.
//
// Verified to fail: routing Close back through xpty's Close makes the returned
// error non-nil on every clean teardown.
func TestCloseReleasesTheMasterAndReportsIt(t *testing.T) {
	sh := shellPath(t)

	p, err := Start(Config{Argv: []string{sh, "-c", "exit 0"}, Cols: 20, Rows: 5}, Handler{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		_ = p.Close()
		t.Fatal("child never finished")
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close reported a failure for a clean teardown: %v", err)
	}
	// Close is idempotent, so the second call must report the same nothing.
	if err := p.Close(); err != nil {
		t.Fatalf("second Close reported %v, want the first call's nil", err)
	}
	// The master really is released, rather than merely reported as such.
	if u, ok := p.pty.(*xpty.UnixPty); ok {
		if _, err := u.Master().Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("reading the master after Close gave %v, want %v", err, os.ErrClosed)
		}
	}
}

// TestCloseIsIdempotentUnderConcurrentReaders runs teardown against the
// accessors a cleanup hook races with in practice. Everything here is about
// what -race reports; the assertion at the end only confirms the child was
// still reaped correctly while the readers were hammering it.
func TestCloseIsIdempotentUnderConcurrentReaders(t *testing.T) {
	sh := shellPath(t)

	for i := range 20 {
		p, err := Start(Config{Argv: []string{sh, "-c", `printf 'hi\n'; exit 5`}, Cols: 20, Rows: 5}, Handler{
			OnData: func([]byte) {},
		})
		if err != nil {
			t.Fatalf("run %d: start: %v", i, err)
		}

		select {
		case <-p.Done():
		case <-time.After(10 * time.Second):
			_ = p.Close()
			t.Fatalf("run %d: child never finished", i)
		}

		var wg sync.WaitGroup
		errs := make([]error, 4)
		for c := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[c] = p.Close()
			}()
		}
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 500 {
					_, _ = p.ExitCode()
					_, _ = p.ExitStatus()
					_ = p.Pid()
				}
			}()
		}
		wg.Wait()

		for c, err := range errs {
			if err != nil {
				t.Fatalf("run %d: concurrent Close %d reported %v", i, c, err)
			}
		}
		if code, exited := p.ExitCode(); !exited || code != 5 {
			t.Fatalf("run %d: ExitCode() = (%d, %v), want (5, true)", i, code, exited)
		}
	}
}
