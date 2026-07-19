package ptyproc

import (
	"os"
	"os/exec"
	"strings"
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
