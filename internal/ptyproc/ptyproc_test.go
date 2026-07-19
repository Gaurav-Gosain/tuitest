package ptyproc

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
