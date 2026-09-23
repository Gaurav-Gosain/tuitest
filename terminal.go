// Package tuitest is a headless testing harness for terminal programs. It
// drives a program under test through a real pseudo-terminal, interprets its
// output with a VT emulator, and lets tests assert on the resulting screen as a
// grid of cells rather than as a raw byte stream.
//
// The typical flow: Start (or StartT under go test) spawns the program, SendKeys
// and Type drive input, the WaitFor family synchronizes on screen state without
// sleeping, and Snapshot / AssertGolden capture the result. Close (registered
// automatically by StartT) tears down the whole process group.
package tuitest

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
	"github.com/Gaurav-Gosain/tuitest/internal/ptyproc"
)

// DefaultStabilizeInterval is the quiet window WaitStable uses unless overridden.
const DefaultStabilizeInterval = 150 * time.Millisecond

// pollInterval bounds how long a wait blocks between re-checks when no output
// arrives. Output-driven wakeups fire immediately; this only backstops
// wall-clock conditions such as WaitStable.
const pollInterval = 5 * time.Millisecond

type config struct {
	cols, rows int
	env        []string
	inheritEnv bool
	dir        string
	term       string
	trueColor  bool
	log        io.Writer
	outMirror  io.Writer
	semantic   bool
	stabilize  time.Duration
}

func defaultConfig() config {
	return config{
		cols:      80,
		rows:      24,
		term:      "xterm-256color",
		stabilize: DefaultStabilizeInterval,
	}
}

// Option configures a spawn.
type Option func(*config)

// WithSize sets the initial PTY size in cells. Both must be between 1 and
// 65535, the range the kernel can store; Start refuses anything else.
func WithSize(cols, rows int) Option {
	return func(c *config) { c.cols, c.rows = cols, rows }
}

// WithEnv adds or overrides environment entries ("KEY=VALUE").
func WithEnv(kv ...string) Option {
	return func(c *config) { c.env = append(c.env, kv...) }
}

// WithInheritEnv starts from the parent process environment instead of the
// minimal hermetic default.
func WithInheritEnv() Option {
	return func(c *config) { c.inheritEnv = true }
}

// WithDir sets the child's working directory.
func WithDir(path string) Option {
	return func(c *config) { c.dir = path }
}

// WithTerm overrides the TERM value (default "xterm-256color").
func WithTerm(term string) Option {
	return func(c *config) { c.term = term }
}

// WithTrueColor sets COLORTERM=truecolor for programs that gate 24-bit color.
func WithTrueColor() Option {
	return func(c *config) { c.trueColor = true }
}

// WithLog mirrors all PTY I/O to w for debugging failing tests.
func WithLog(w io.Writer) Option {
	return func(c *config) { c.log = w }
}

// WithSemanticMarkers enables OSC 133 tracking so the WaitForPrompt /
// WaitForCommand / LastCommandExit primitives work.
func WithSemanticMarkers() Option {
	return func(c *config) { c.semantic = true }
}

// WithStabilizeInterval sets the quiet window used by WaitStable.
func WithStabilizeInterval(d time.Duration) Option {
	return func(c *config) { c.stabilize = d }
}

// WithOutputMirror copies the child's output to w as it arrives. Unlike
// WithLog, which mirrors both directions for debugging, this carries only what
// the program wrote, so w can be a real terminal the program is rendered onto
// while the harness still drives it headlessly. Used by tuitest record and
// tuitest replay.
func WithOutputMirror(w io.Writer) Option {
	return func(c *config) { c.outMirror = w }
}

// Terminal is the harness handle for one spawned program.
type Terminal struct {
	cfg  config
	proc *ptyproc.Process
	emu  emu.Emulator

	mu        sync.Mutex
	cond      *sync.Cond
	lastWrite time.Time // last byte received from the child
	lastInput time.Time // last byte sent to the child
	outBytes  int64     // total bytes read from the child
	exited    bool
	exitCode  int
	// pendingResp holds emulator responses produced before proc was stored.
	pendingResp []byte

	log       io.Writer
	logMu     sync.Mutex
	outMirror io.Writer
	tailBuf   []byte // ring of recent I/O for error dumps
}

const tailCap = 4 * 1024

// Start spawns argv[0] with argv[1:] in a PTY and begins pumping output.
func Start(argv []string, opts ...Option) (*Terminal, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if err := checkSize("WithSize", cfg.cols, cfg.rows); err != nil {
		return nil, err
	}

	t := &Terminal{
		cfg:       cfg,
		emu:       emu.New(cfg.cols, cfg.rows),
		log:       cfg.log,
		outMirror: cfg.outMirror,
		exitCode:  -1,
		lastWrite: time.Now(), // measure the first quiet window from spawn
	}
	t.cond = sync.NewCond(&t.mu)

	proc, err := ptyproc.Start(ptyproc.Config{
		Argv: argv,
		Env:  cfg.buildEnv(),
		Dir:  cfg.dir,
		Cols: cfg.cols,
		Rows: cfg.rows,
	}, ptyproc.Handler{
		OnData:  t.onData,
		OnClose: t.onClose,
	})
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.proc = proc
	t.mu.Unlock()
	// Flush anything the emulator answered while proc was still unset.
	t.sendResponses(nil)
	return t, nil
}

// StartT is the testing.TB-friendly constructor: it wires the debug log to
// t.Log, registers Close via t.Cleanup, and fails the test on spawn error.
func StartT(tb testing.TB, argv []string, opts ...Option) *Terminal {
	tb.Helper()
	opts = append([]Option{WithLog(testLogWriter{tb})}, opts...)
	term, err := Start(argv, opts...)
	if err != nil {
		tb.Fatalf("tuitest: spawn %v: %v", argv, err)
	}
	tb.Cleanup(func() {
		// A teardown that could not kill everything the program spawned fails
		// the test. Swallowing it here is how a suite leaks processes quietly
		// until the machine it runs on is full of them.
		if err := term.Close(); err != nil {
			tb.Errorf("tuitest: %v", err)
		}
	})
	return term
}

// testLogWriter adapts testing.TB.Log to io.Writer. testing serializes Log per
// test, so parallel tests do not interleave garbled mirror output.
type testLogWriter struct{ tb testing.TB }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.tb.Logf("pty: %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (t *Terminal) onData(p []byte) {
	t.mu.Lock()
	_, _ = t.emu.Write(p)
	t.lastWrite = time.Now()
	t.outBytes += int64(len(p))
	t.appendTailLocked(p)
	t.cond.Broadcast()
	resp := t.emu.TakeResponses()
	t.mu.Unlock()
	t.sendResponses(resp)
	t.mirror(p)
	// The pump is a single goroutine, so mirroring outside the lock still
	// delivers chunks to w in the order the child produced them.
	if t.outMirror != nil {
		_, _ = t.outMirror.Write(p)
	}
}

// sendResponses forwards emulator query answers back to the child. Without
// this a program that probes the terminal before drawing, which most Bubble
// Tea and termenv programs do to detect the background colour, waits out its
// retry loop and renders nothing.
//
// The output pump can deliver data before Start has stored the process handle,
// so responses produced that early are held and flushed once it exists. They
// are not mirrored to the debug log or counted as caller input: they are the
// terminal answering on its own behalf, and treating them as input would keep
// WaitStable from ever settling, since each response arrives with the output
// that provoked it.
func (t *Terminal) sendResponses(resp []byte) {
	t.mu.Lock()
	proc := t.proc
	if proc == nil {
		t.pendingResp = append(t.pendingResp, resp...)
		t.mu.Unlock()
		return
	}
	if len(t.pendingResp) > 0 {
		resp = append(t.pendingResp, resp...)
		t.pendingResp = nil
	}
	t.mu.Unlock()
	if len(resp) == 0 {
		return
	}
	_ = proc.Write(resp)
}

func (t *Terminal) onClose(code int) {
	t.mu.Lock()
	t.exited = true
	t.exitCode = code
	t.cond.Broadcast()
	t.mu.Unlock()
}

func (t *Terminal) mirror(p []byte) {
	if t.log == nil {
		return
	}
	t.logMu.Lock()
	_, _ = t.log.Write(p)
	t.logMu.Unlock()
}

func (t *Terminal) appendTailLocked(p []byte) {
	t.tailBuf = append(t.tailBuf, p...)
	if len(t.tailBuf) > tailCap {
		t.tailBuf = append([]byte(nil), t.tailBuf[len(t.tailBuf)-tailCap:]...)
	}
}

// snapshotLocked builds an immutable copy of the grid. Caller must hold t.mu.
func (t *Terminal) snapshotLocked() *screenSnapshot {
	cols, rows := t.emu.Size()
	cells := make([][]Cell, rows)
	for row := 0; row < rows; row++ {
		line := make([]Cell, cols)
		for col := 0; col < cols; col++ {
			line[col] = toCell(t.emu.CellAt(col, row))
		}
		cells[row] = line
	}
	curCol, curRow, curVis := t.emu.Cursor()
	code, exited := t.exitLocked()
	return &screenSnapshot{
		cols:       cols,
		rows:       rows,
		cells:      cells,
		curCol:     curCol,
		curRow:     curRow,
		curVisible: curVis,
		exitCode:   code,
		exited:     exited,
	}
}

// exitLocked reports the exit state from whichever source knows about it first.
// Caller must hold t.mu.
//
// The pump reaps the child, then hands the code to onClose, then closes Done, so
// for the length of that callback the process knows the child is gone and this
// terminal does not. Falling back to the process covers that window, and every
// accessor then answers the same thing throughout it, instead of Screen
// reporting a running program while ExitCode reports a finished one.
//
// Once onClose has run the local copy is authoritative and the process is never
// consulted, which keeps the waits that rebuild a snapshot on every wakeup off
// the process's lock.
func (t *Terminal) exitLocked() (int, bool) {
	if t.exited {
		return t.exitCode, true
	}
	if t.proc != nil {
		if code, exited := t.proc.ExitCode(); exited {
			return code, true
		}
	}
	return t.exitCode, false
}

// Screen returns an immutable view of the current screen.
func (t *Terminal) Screen() Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

// Type sends literal text with no key-name interpretation (tmux send-keys -l).
func (t *Terminal) Type(s string) error {
	return t.write([]byte(s))
}

// write mirrors input to the debug log, records that input was sent (which is
// what keeps WaitStable from reporting the pre-input screen as stable), then
// sends it to the child.
func (t *Terminal) write(b []byte) error {
	t.mirror(b)
	t.markInput()
	return t.inputErr("write", t.proc.Write(b))
}

// exitSettleGrace bounds how long a failed write or resize waits for the child
// to be reaped before the error is returned as it stands. The pump reaps as
// soon as it has drained the PTY, so a child that is really exiting is reaped
// in well under this. A child that closed its terminal and kept running is
// never reaped, and the bound is what keeps a write to it from blocking.
const exitSettleGrace = time.Second

// inputErr turns a failed write or resize into an error that says whether the
// child has exited.
//
// On macOS a write to the PTY master fails with EIO as soon as the child has
// closed its end of the terminal, which is before the pump has drained the
// master and reaped it. Returned as it stands, the error arrives while
// ExitStatus still reports a running child, and the caller cannot tell a
// program that quit from a harness that broke. Waiting briefly for the reap
// makes the two consistent, and wrapping ErrChildExited lets the caller branch
// on it. Linux accepts writes to a master whose other end has closed, so there
// the error only appears once Close has released the PTY.
//
// The pump never comes through here. It writes emulator responses with
// proc.Write directly, and waiting for Done from the pump would wait on
// itself.
func (t *Terminal) inputErr(op string, err error) error {
	if err == nil {
		return nil
	}
	select {
	case <-t.proc.Done():
	case <-time.After(exitSettleGrace):
		return err
	}
	st, _ := t.ExitStatus()
	return &exitedInputError{op: op, status: st, err: err}
}

// exitedInputError is a write or resize that failed because the child has
// exited. It wraps both ErrChildExited and the underlying I/O error.
type exitedInputError struct {
	op     string
	status ExitStatus
	err    error
}

func (e *exitedInputError) Error() string {
	return "tuitest: " + e.op + " failed: the child has exited (" + e.status.String() + "): " + e.err.Error()
}

func (e *exitedInputError) Unwrap() []error { return []error{ErrChildExited, e.err} }

// markInput timestamps the moment input was handed to the child.
func (t *Terminal) markInput() {
	t.mu.Lock()
	t.lastInput = time.Now()
	t.mu.Unlock()
}

// maxSize is the largest width or height a PTY can carry: the kernel keeps the
// window size in 16-bit fields.
const maxSize = 1<<16 - 1

// checkSize refuses a size the emulator and the PTY would not agree on. Zero
// and negative sizes have no meaning, and anything past maxSize is silently
// truncated by the kernel while the emulator takes it as given, so the program
// would draw for one size and the screen would be another.
func checkSize(op string, cols, rows int) error {
	if cols < 1 || rows < 1 || cols > maxSize || rows > maxSize {
		return fmt.Errorf("tuitest: %s: size %dx%d is out of range; columns and rows must be between 1 and %d", op, cols, rows, maxSize)
	}
	return nil
}

// Resize changes the PTY window size and the emulator grid; the child receives
// SIGWINCH. Like sending keys, a resize counts as input for WaitStable, since
// the redraw it provokes has not arrived yet.
func (t *Terminal) Resize(cols, rows int) error {
	if err := checkSize("Resize", cols, rows); err != nil {
		return err
	}
	t.mu.Lock()
	t.emu.Resize(cols, rows)
	t.lastInput = time.Now()
	t.mu.Unlock()
	return t.inputErr("resize", t.proc.Resize(cols, rows))
}

// Progress reports how many bytes the child has written so far and when the
// most recent write landed. A caller that sends input and then sees neither
// counter move has evidence the program stopped responding.
func (t *Terminal) Progress() (bytes int64, last time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outBytes, t.lastWrite
}

// ExitCode reports the child's exit code and whether it has exited.
//
// The code is -1 for a child killed by a signal, not the shell's 128+signal
// convention, so that it can never be confused with a program that exited with
// that number itself. It is -1 before the child has exited too, and the second
// return value is the only thing that separates those two cases. Use ExitStatus
// to tell a crash apart from an ordinary non-zero exit.
func (t *Terminal) ExitCode() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitLocked()
}

// WaitExit blocks until the child exits or timeout elapses, returning the exit
// code. On timeout it returns -1 and a *TimeoutError, which unwraps to
// ErrTimeout like every other wait. A child killed by a signal also returns -1,
// with a nil error; ExitStatus is what tells the two apart.
func (t *Terminal) WaitExit(timeout time.Duration) (int, error) {
	// Wait on the terminal's own view of the exit rather than on the process
	// handle. onClose sets it only after every byte the child wrote has been fed
	// to the emulator, so a caller that snapshots the screen the moment this
	// returns sees the program's final frame and not one chunk short of it.
	err := t.waitLoop("WaitExit", "the child to exit", timeout, true, func() bool {
		return t.exited
	})
	if err != nil {
		return -1, err
	}
	code, _ := t.ExitCode()
	return code, nil
}

// Wait is the former name of WaitExit, kept so existing tests keep compiling.
//
// Deprecated: use WaitExit. "Wait" read as a sibling of the WaitFor family,
// which wait on screen state, when in fact it waits for process exit.
func (t *Terminal) Wait(timeout time.Duration) (int, error) {
	return t.WaitExit(timeout)
}

// Done returns a channel closed once the child has exited and been reaped. It
// lets a caller select on program exit alongside its own events, which Wait
// cannot express because it blocks.
func (t *Terminal) Done() <-chan struct{} { return t.proc.Done() }

// Close tears down the child process group and PTY. It is idempotent.
func (t *Terminal) Close() error {
	if t.proc == nil {
		return nil
	}
	return t.proc.Close()
}

func (c config) buildEnv() []string {
	set := map[string]string{}
	order := []string{}
	put := func(kv string) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		if _, seen := set[k]; !seen {
			order = append(order, k)
		}
		set[k] = v
	}

	if c.inheritEnv {
		for _, kv := range os.Environ() {
			put(kv)
		}
	} else {
		// Minimal hermetic base.
		if p := os.Getenv("PATH"); p != "" {
			put("PATH=" + p)
		}
		if h := os.Getenv("HOME"); h != "" {
			put("HOME=" + h)
		}
		put("LANG=C.UTF-8")
	}

	put("TERM=" + c.term)
	if c.trueColor {
		put("COLORTERM=truecolor")
	}
	for _, kv := range c.env {
		put(kv)
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+set[k])
	}
	return out
}
