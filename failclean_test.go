package tuitest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// These tests cover the ways one bad test used to take the whole run with it:
// an emulator panic, a write to a program that stopped reading, a log that
// grows with the program's output, and a slow teardown.

func binSh(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/bin/sh", "/usr/bin/sh"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no POSIX shell available")
	return ""
}

// panickyEmu panics when a chunk of output holds trigger. It stands in for a
// bug in the vendored emulator.
type panickyEmu struct {
	emu.Emulator
	trigger []byte
}

func (e panickyEmu) Write(p []byte) (int, error) {
	if bytes.Contains(p, e.trigger) {
		panic("injected emulator bug")
	}
	return e.Emulator.Write(p)
}

func withPanickyEmu(trigger string) Option {
	return func(c *config) {
		c.newEmu = func(cols, rows int) emu.Emulator {
			return panickyEmu{Emulator: emu.New(cols, rows), trigger: []byte(trigger)}
		}
	}
}

// fakeTB records what StartT does with a test, and runs its cleanups on
// demand.
type fakeTB struct {
	testing.TB
	mu       sync.Mutex
	failed   bool
	errs     []string
	logs     []string
	cleanups []func()
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = true
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.Errorf(format, args...)
	panic("fakeTB.Fatalf")
}
func (f *fakeTB) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Failed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failed
}
func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }
func (f *fakeTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
	f.cleanups = nil
}
func (f *fakeTB) loggedBytes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.logs {
		n += len(l)
	}
	return n
}

// An emulator panic must fail the calls made after it, not end the test
// binary. Before the recover in feedLocked, the panic ran on the pump
// goroutine, which no test can recover, and this test binary died with it.
//
// The program writes 2 MB after the chunk that panics and then exits. That is
// far more than a PTY buffers, so it exits only if the terminal keeps reading
// its output after the panic.
func TestEmulatorPanicFailsCleanly(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	script := `printf 'ready\n'; read x; printf 'BOOM\n'; head -c 2000000 /dev/zero; exit 7`
	term, err := Start([]string{sh, "-c", script}, WithSize(40, 6), withPanickyEmu("BOOM"))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	if err := term.WaitForText("ready", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("go\n"); err != nil {
		t.Fatal(err)
	}

	err = term.WaitForText("never drawn", 10*time.Second)
	var pe *EmulatorPanicError
	if !errors.As(err, &pe) || !errors.Is(err, ErrEmulatorPanic) {
		t.Fatalf("wait after the panic: got %v, want an *EmulatorPanicError", err)
	}
	if pe.Op != "WaitForText" || pe.Value != "injected emulator bug" || !bytes.Contains(pe.Chunk, []byte("BOOM")) {
		t.Errorf("error fields: Op=%q Value=%v Chunk=%q", pe.Op, pe.Value, pe.Chunk)
	}
	if !strings.Contains(pe.Screen, "ready") {
		t.Errorf("the error lost the last screen:\n%s", pe.Screen)
	}
	if !strings.Contains(pe.Stack, "panickyEmu") {
		t.Errorf("the stack does not name the panicking emulator:\n%s", pe.Stack)
	}

	select {
	case <-term.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the program did not exit: the terminal stopped reading its output after the panic")
	}
	if code, exited := term.ExitCode(); !exited || code != 7 {
		t.Errorf("ExitCode = %d, %v; want 7, true", code, exited)
	}
	if _, err := term.WaitExit(time.Second); !errors.Is(err, ErrEmulatorPanic) {
		t.Errorf("WaitExit after the panic: %v", err)
	}
	if err := term.Type("x"); !errors.Is(err, ErrEmulatorPanic) {
		t.Errorf("Type after the panic: %v", err)
	}
	if err := term.Resize(50, 6); !errors.Is(err, ErrEmulatorPanic) {
		t.Errorf("Resize after the panic: %v", err)
	}
	if s := term.Snapshot(); !strings.Contains(s, "ready") {
		t.Errorf("Snapshot after the panic:\n%s", s)
	}
	if err := term.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// A test that never waits after the panic still fails, through StartT.
func TestEmulatorPanicFailsTheTestThroughStartT(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	tb := &fakeTB{}
	term := StartT(tb, []string{sh, "-c", `printf 'BOOM\n'; exit 0`}, WithSize(40, 6), withPanickyEmu("BOOM"))
	select {
	case <-term.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the program did not exit")
	}
	tb.runCleanups()
	if !tb.Failed() || len(tb.errs) != 1 || !strings.Contains(tb.errs[0], "the terminal emulator panicked") {
		t.Fatalf("StartT cleanup: failed=%v errors=%q", tb.Failed(), tb.errs)
	}
}

// Input to a program that does not read it must time out with the screen,
// not block for ever. The program here never reads, so the PTY input buffer
// fills after a few KB and the write of 1 MiB blocks.
func TestWriteToNonReaderTimesOut(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	term, err := Start([]string{sh, "-c", `stty raw -echo; printf 'ready\n'; exec sleep 60`},
		WithSize(40, 6), WithWriteTimeout(500*time.Millisecond), WithKillGrace(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if err := term.WaitForText("ready", 10*time.Second); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res := make(chan error, 1)
	go func() { res <- term.Type(strings.Repeat("x", 1<<20)) }()
	select {
	case err = <-res:
	case <-time.After(10 * time.Second):
		t.Fatal("Type of 1 MiB to a program that does not read is still blocked after 10s")
	}
	elapsed := time.Since(start)
	var te *TimeoutError
	if !errors.As(err, &te) || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Type: got %v, want a *TimeoutError", err)
	}
	if te.Op != "Type" || !strings.Contains(te.Screen, "ready") {
		t.Errorf("TimeoutError: Op=%q Screen=\n%s", te.Op, te.Screen)
	}
	if elapsed < 500*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("Type returned after %s, want about the 500ms write timeout", elapsed)
	}
	t.Logf("Type of 1 MiB returned a TimeoutError after %s", elapsed.Round(time.Millisecond))

	// Part of the input is in the buffer, so later input is refused at once.
	start = time.Now()
	err = term.SendKeys("y")
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "accepts no more input") {
		t.Errorf("SendKeys after the timeout: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("SendKeys after the timeout took %s, want an immediate refusal", d)
	}

	// Close must return, and end the program, while the write is blocked.
	// It does not end the write: see sendInputBounded.
	closed := make(chan error, 1)
	go func() { closed <- term.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Close is blocked behind the stalled write")
	}
}

// The write timeout also covers the wait for inputMu. The goroutine that
// forwards query answers holds that lock while it writes, so a write to a
// program that stopped reading blocks it there, and any input sent after it
// waits for the lock.
func TestWriteTimeoutCoversTheInputLock(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	term, err := Start([]string{sh, "-c", `printf 'ready\n'; exec sleep 60`},
		WithSize(40, 6), WithWriteTimeout(300*time.Millisecond), WithKillGrace(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if err := term.WaitForText("ready", 10*time.Second); err != nil {
		t.Fatal(err)
	}

	// Stand in for the answer goroutine, blocked in its write.
	term.inputMu.Lock()
	res := make(chan error, 1)
	go func() { res <- term.Type("x") }()
	select {
	case err = <-res:
	case <-time.After(10 * time.Second):
		term.inputMu.Unlock()
		t.Fatal("Type is still waiting for the input lock after 10s")
	}
	term.inputMu.Unlock()
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Type while the input lock is held: got %v, want a *TimeoutError", err)
	}
}

// brittleEmu stands in for an emulator bug that is not in Write. Once a chunk
// of output holds trigger, method panics. With method "Write", that chunk
// panics and every later read panics too, as a broken emulator's might.
type brittleEmu struct {
	emu.Emulator
	trigger []byte
	method  string
	armed   *atomic.Bool
}

func withBrittleEmu(trigger, method string) Option {
	return func(c *config) {
		c.newEmu = func(cols, rows int) emu.Emulator {
			return brittleEmu{Emulator: emu.New(cols, rows), trigger: []byte(trigger), method: method, armed: new(atomic.Bool)}
		}
	}
}

func (e brittleEmu) check(method string) {
	if e.armed.Load() && (e.method == method || e.method == "Write") {
		panic("injected emulator bug in " + method)
	}
}

func (e brittleEmu) Write(p []byte) (int, error) {
	if bytes.Contains(p, e.trigger) {
		e.armed.Store(true)
		e.check("Write")
	}
	return e.Emulator.Write(p)
}
func (e brittleEmu) Resize(cols, rows int)    { e.check("Resize"); e.Emulator.Resize(cols, rows) }
func (e brittleEmu) Size() (int, int)         { e.check("Size"); return e.Emulator.Size() }
func (e brittleEmu) Cursor() (int, int, bool) { e.check("Cursor"); return e.Emulator.Cursor() }
func (e brittleEmu) Modes() map[int]bool      { e.check("Modes"); return e.Emulator.Modes() }
func (e brittleEmu) PromptCount() int         { e.check("PromptCount"); return e.Emulator.PromptCount() }
func (e brittleEmu) CommandFinishedCount() int {
	e.check("CommandFinishedCount")
	return e.Emulator.CommandFinishedCount()
}
func (e brittleEmu) LastCommandExit() (int, bool) {
	e.check("LastCommandExit")
	return e.Emulator.LastCommandExit()
}
func (e brittleEmu) ApplicationCursorKeys() bool {
	e.check("ApplicationCursorKeys")
	return e.Emulator.ApplicationCursorKeys()
}

// callNoPanic runs fn on its own goroutine and fails the test if fn panics or
// does not return. A panic that escapes a call made with t.mu held, and no
// deferred unlock, leaves every later call blocked, so the bound matters.
func callNoPanic(t *testing.T, name string, fn func()) bool {
	t.Helper()
	res := make(chan any, 1)
	go func() {
		defer func() { res <- recover() }()
		fn()
	}()
	select {
	case v := <-res:
		if v != nil {
			t.Errorf("%s panicked on the test goroutine: %v", name, v)
			return false
		}
		return true
	case <-time.After(5 * time.Second):
		t.Errorf("%s is still blocked after 5s", name)
		return false
	}
}

// A panic in Resize, or in a read of the screen, is recovered like one in
// Write. Before, it reached the test goroutine with t.mu held.
func TestEmulatorPanicOutsideWriteFailsCleanly(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	for _, tc := range []struct {
		method string
		op     string
		during string
		call   func(*Terminal) error
	}{
		{"Resize", "Resize", "resizing to 50x6", func(term *Terminal) error { return term.Resize(50, 6) }},
		{"Cursor", "WaitForText", "copying the screen", func(term *Terminal) error {
			return term.WaitForText("never drawn", 10*time.Second)
		}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			term, err := Start([]string{sh, "-c", `printf 'ARM\n'; exec sleep 60`},
				WithSize(40, 6), WithKillGrace(100*time.Millisecond), withBrittleEmu("ARM", tc.method))
			if err != nil {
				t.Fatal(err)
			}
			defer term.Close()
			// Wait for the arming output without reading the screen.
			deadline := time.Now().Add(10 * time.Second)
			for out, _ := term.Progress(); out < 3; out, _ = term.Progress() {
				if time.Now().After(deadline) {
					t.Fatal("the program wrote nothing")
				}
				time.Sleep(10 * time.Millisecond)
			}

			var callErr error
			if !callNoPanic(t, tc.op, func() { callErr = tc.call(term) }) {
				return
			}
			var pe *EmulatorPanicError
			if !errors.As(callErr, &pe) {
				t.Fatalf("%s: got %v, want an *EmulatorPanicError", tc.op, callErr)
			}
			if pe.Op != tc.op || pe.During != tc.during || len(pe.Chunk) != 0 {
				t.Errorf("error fields: Op=%q During=%q Chunk=%q", pe.Op, pe.During, pe.Chunk)
			}
			// The lock was released: later calls return.
			callNoPanic(t, "Snapshot", func() { _ = term.Snapshot() })
			callNoPanic(t, "Type", func() {
				if err := term.Type("x"); !errors.Is(err, ErrEmulatorPanic) {
					t.Errorf("Type after the panic: %v", err)
				}
			})
		})
	}
}

// After a panic in Write, the calls that read the emulator outside the waits
// must not read it again. A broken emulator can panic on any read, and before,
// that panic reached the test goroutine.
func TestReadsAfterEmulatorPanicDoNotPanic(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	term, err := Start([]string{sh, "-c", `printf 'ready\n'; read x; printf 'BOOM\n'; exec sleep 60`},
		WithSize(40, 6), WithKillGrace(100*time.Millisecond), WithSemanticMarkers(), withBrittleEmu("BOOM", "Write"))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if err := term.WaitForText("ready", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("go\n"); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("never drawn", 10*time.Second); !errors.Is(err, ErrEmulatorPanic) {
		t.Fatalf("wait after the panic: %v", err)
	}

	callNoPanic(t, "TermState", func() { _ = term.TermState() })
	callNoPanic(t, "LastCommandExit", func() { _, _ = term.LastCommandExit() })
	callNoPanic(t, "Screen", func() { _ = term.Screen() })
	for name, call := range map[string]func() error{
		"WaitForPrompt":  func() error { return term.WaitForPrompt(time.Second) },
		"WaitForCommand": func() error { return term.WaitForCommand(time.Second) },
		"SendKeys":       func() error { return term.SendKeys(Up) },
	} {
		callNoPanic(t, name, func() {
			if err := call(); !errors.Is(err, ErrEmulatorPanic) {
				t.Errorf("%s after the panic: %v", name, err)
			}
		})
	}
}

// StartT keeps a bounded tail of the PTY I/O and logs it only on failure. It
// used to copy every chunk to t.Log, so a failing test whose program printed 2
// MB logged 2.27 MB of escape sequences.
func TestStartTLogsABoundedTailOnFailure(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	// 2 MB of lines, then a marker the tail must end with.
	script := `yes "$(head -c 999 /dev/zero | tr '\0' a)" | head -n 2000; printf 'END-OF-FLOOD\n'; exec sleep 60`

	run := func(fail bool, opts ...Option) *fakeTB {
		tb := &fakeTB{}
		opts = append([]Option{WithSize(80, 24), WithKillGrace(100 * time.Millisecond)}, opts...)
		term := StartT(tb, []string{sh, "-c", script}, opts...)
		if err := term.WaitForText("END-OF-FLOOD", 60*time.Second); err != nil {
			t.Fatal(err)
		}
		if fail {
			tb.Errorf("the test failed")
		}
		tb.runCleanups()
		return tb
	}

	failed := run(true)
	n := failed.loggedBytes()
	t.Logf("a failing test with 2 MB of output logged %d bytes", n)
	if n > 64*1024 {
		t.Errorf("a failing test logged %d bytes, want at most 64 KB", n)
	}
	if n == 0 || !strings.Contains(failed.logs[len(failed.logs)-1], "END-OF-FLOOD") {
		t.Errorf("the log of a failing test does not end with the last output")
	}
	checkLogEscaped(t, "the tail", failed.logs[len(failed.logs)-1:])

	if passed := run(false); passed.loggedBytes() != 0 {
		t.Errorf("a passing test logged %d bytes, want none", passed.loggedBytes())
	}

	full := run(false, WithFullTestLog())
	if full.loggedBytes() < 2_000_000 {
		t.Errorf("WithFullTestLog logged %d bytes, want all 2 MB", full.loggedBytes())
	}
	checkLogEscaped(t, "WithFullTestLog", full.logs)
}

// checkLogEscaped fails the test if logs hold a raw control byte, or a PTY
// line without the "pty: " prefix. Raw, the program's escape sequences act on
// the terminal that shows the go test output.
func checkLogEscaped(t *testing.T, what string, logs []string) {
	t.Helper()
	for _, entry := range logs {
		for i, line := range strings.Split(entry, "\n") {
			if i == 0 && strings.HasPrefix(line, "tuitest: last ") {
				continue // the header of the tail
			}
			if !strings.HasPrefix(line, "pty: ") || strings.ContainsFunc(line, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				t.Errorf("%s logged a raw line: %q", what, line[:min(len(line), 80)])
				return
			}
		}
	}
}

// The description given to WaitForDesc names the condition in the error.
func TestWaitForDescNamesTheCondition(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	term, err := Start([]string{sh, "-c", `printf 'ready\n'; exec sleep 60`}, WithSize(40, 6), WithKillGrace(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	const desc = "the status bar to show 3 windows"
	err = term.WaitForDesc(desc, func(s Screen) bool { return false }, 100*time.Millisecond)
	var te *TimeoutError
	if !errors.As(err, &te) || te.Op != "WaitForDesc" || te.Want != desc || !strings.Contains(err.Error(), "waiting for "+desc) {
		t.Fatalf("WaitForDesc: got %v, want a TimeoutError for %q", err, desc)
	}
}

// WithKillGrace bounds how long Close waits for a program that ignores
// SIGTERM. The fixed grace was 2s, so each such Close cost 2.04s.
func TestKillGraceBoundsClose(t *testing.T) {
	t.Parallel()
	sh := binSh(t)
	term, err := Start([]string{sh, "-c", `trap '' TERM; printf 'ready\n'; exec sleep 60`},
		WithSize(40, 6), WithKillGrace(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("ready", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	t.Logf("Close of a program that ignores SIGTERM took %s with a 200ms grace", d.Round(time.Millisecond))
	if d > 1500*time.Millisecond {
		t.Errorf("Close took %s, want about the 200ms grace", d)
	}
	if st, _ := term.ExitStatus(); !st.Signaled || st.Signal != syscall.SIGKILL {
		t.Errorf("exit status %v, want killed by SIGKILL", st)
	}
}
