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
	"sync/atomic"
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

	// Answers to the program's terminal queries wait in respQ until they are
	// written. respMu guards respQ, and respWake nudges the goroutine that
	// writes them. inputMu is held across every write to the PTY, so the
	// answers and the caller's input reach the program in the order they
	// were produced. See queueResponses.
	respMu   sync.Mutex
	respQ    []byte
	respWake chan struct{}
	inputMu  sync.Mutex
	// inBytes counts every byte written to the child's input, the caller's and
	// the answers alike. It is atomic rather than under mu so that a caller can
	// read it from inside a wait condition, which runs with mu held.
	inBytes atomic.Int64

	// gen counts changes to the emulator grid. The snapshot cache is valid for
	// as long as gen has not moved and the exit state it recorded still holds.
	gen      uint64
	cache    *screenSnapshot
	cacheGen uint64

	// presented is the last complete frame while the program has a
	// synchronized update (DEC mode 2026) open, and nil otherwise. syncSince is
	// when that update opened. See viewLocked.
	presented *screenSnapshot
	syncSince time.Time

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

	t := newTerminal(cfg)
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
	// The first thing it does is write anything the emulator answered while
	// proc was still unset.
	go t.respond(proc)
	return t, nil
}

// newTerminal builds a Terminal with its emulator and no process attached.
func newTerminal(cfg config) *Terminal {
	t := &Terminal{
		cfg:       cfg,
		emu:       emu.New(cfg.cols, cfg.rows),
		log:       cfg.log,
		outMirror: cfg.outMirror,
		exitCode:  -1,
		lastWrite: time.Now(), // measure the first quiet window from spawn
		respWake:  make(chan struct{}, 1),
	}
	t.cond = sync.NewCond(&t.mu)
	// Called from inside emu.Write, so t.mu is already held.
	t.emu.OnSync(t.syncChangedLocked)
	return t
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
	t.gen++
	t.lastWrite = time.Now()
	t.outBytes += int64(len(p))
	t.appendTailLocked(p)
	t.cond.Broadcast()
	t.queueResponses(t.emu.TakeResponses())
	t.mu.Unlock()
	t.mirror(p)
	// The pump is a single goroutine, so mirroring outside the lock still
	// delivers chunks to w in the order the child produced them.
	if t.outMirror != nil {
		_, _ = t.outMirror.Write(p)
	}
}

// maxQueuedResponses bounds the answers held for a program that asks faster
// than it reads. Past it new answers are dropped, which is what a program that
// never reads its input gets from a real terminal too, eventually.
const maxQueuedResponses = 1 << 20

// queueResponses hands emulator query answers to the goroutine that writes them
// back to the child. Without them a program that probes the terminal before
// drawing, which most Bubble Tea and termenv programs do to detect the
// background colour, waits out its retry loop and renders nothing. Caller holds
// t.mu, which keeps the answers in the order the emulator produced them.
//
// The answers are queued rather than written here because this runs on the
// output pump, and a write to the PTY blocks once the program's input buffer is
// full. A program that sends queries faster than it reads the answers, or that
// sends a burst of them and reads nothing, filled that buffer, the pump stopped
// reading, the program then blocked writing its output, and the two waited on
// each other until the test timed out. A real terminal keeps reading output
// whatever the state of the input side.
//
// The answers are not mirrored to the debug log or counted as caller input:
// they are the terminal answering on its own behalf, and treating them as input
// would keep WaitStable from ever settling, since each one arrives with the
// output that provoked it.
func (t *Terminal) queueResponses(resp []byte) {
	if len(resp) == 0 {
		return
	}
	t.respMu.Lock()
	if len(t.respQ)+len(resp) <= maxQueuedResponses {
		t.respQ = append(t.respQ, resp...)
	}
	t.respMu.Unlock()
	select {
	case t.respWake <- struct{}{}:
	default: // a wakeup is already pending
	}
}

// takeResponsesLocked removes and returns the queued answers. Caller holds
// t.inputMu, so nothing else writes to the PTY between taking them and
// writing them.
func (t *Terminal) takeResponsesLocked() []byte {
	t.respMu.Lock()
	q := t.respQ
	t.respQ = nil
	t.respMu.Unlock()
	return q
}

// respond writes queued answers to the child until it exits.
func (t *Terminal) respond(proc *ptyproc.Process) {
	for {
		t.inputMu.Lock()
		if q := t.takeResponsesLocked(); len(q) > 0 {
			if proc.Write(q) == nil {
				t.inBytes.Add(int64(len(q)))
			}
		}
		t.inputMu.Unlock()
		select {
		case <-t.respWake:
		case <-proc.Done():
			return
		}
	}
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

// syncHoldLimit bounds how long a synchronized update keeps the previous frame
// on show. A program that opens an update and never closes it, because it
// crashed mid-frame or has a bug, must not freeze what the harness reports.
// Real terminals give up after a similar interval and present what they have.
const syncHoldLimit = time.Second

// syncChangedLocked is called by the emulator, from inside Write, when the
// program opens or closes a synchronized update (DEC mode 2026). Caller holds
// t.mu.
//
// Opening one captures the grid as it stands, before any byte of the new frame
// has been applied: that is the frame a real terminal keeps on screen until the
// update closes. Programs that draw with synchronized output, which includes
// every Bubble Tea v2 program talking to a terminal that answers the mode query
// the way this one does, rely on that to never show a half-drawn frame. The
// PTY hands a frame over in pieces (on macOS in chunks of about a kilobyte), so
// without this a wait could match text at the top of a new frame and the next
// read could find the bottom half of the previous one.
func (t *Terminal) syncChangedLocked(open bool) {
	if open {
		// Built fresh rather than through the cache: the bytes of this chunk
		// that came before the mode change are already on the grid, and gen
		// has not been advanced for them yet.
		code, exited := t.exitLocked()
		t.presented = t.buildSnapshotLocked(code, exited)
		t.syncSince = time.Now()
		return
	}
	t.presented = nil
}

// viewLocked returns the screen a user of a real terminal would see now: the
// live grid, or the last complete frame while the program is inside a
// synchronized update. Every public read of the screen goes through it, so a
// wait, Screen, Snapshot and a failure dump always agree. Caller holds t.mu.
//
// The held frame is dropped once the update has been open for syncHoldLimit,
// and as soon as the child has exited, since nothing can close the update
// after that and the final output is what a caller wants to see.
func (t *Terminal) viewLocked() *screenSnapshot {
	if t.presented != nil && time.Since(t.syncSince) < syncHoldLimit {
		if _, exited := t.exitLocked(); !exited {
			return t.presented
		}
	}
	return t.snapshotLocked()
}

// snapshotLocked returns an immutable copy of the live grid. Caller must hold
// t.mu.
//
// The copy is cached until the grid or the exit state changes. Reading the
// screen is what every wait does on every wakeup and what a polling helper does
// in a loop, and on a screen that has not changed since the last read, which is
// the common case, rebuilding it is pure waste: at 120x40 a rebuild costs about
// a tenth of a millisecond and 400KB of garbage. Sharing one copy between
// readers is safe because a snapshot is never modified after it is built.
func (t *Terminal) snapshotLocked() *screenSnapshot {
	code, exited := t.exitLocked()
	if c := t.cache; c != nil && t.cacheGen == t.gen && c.exited == exited && c.exitCode == code {
		return c
	}
	s := t.buildSnapshotLocked(code, exited)
	t.cache, t.cacheGen = s, t.gen
	return s
}

// buildSnapshotLocked copies the grid out of the emulator. Caller must hold
// t.mu.
func (t *Terminal) buildSnapshotLocked(code int, exited bool) *screenSnapshot {
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
//
// While the program is inside a synchronized update (DEC mode 2026) this is the
// last complete frame, not the half-drawn one being built, which is what a real
// terminal shows too. The waits read the screen the same way.
func (t *Terminal) Screen() Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.viewLocked()
}

// Type sends literal text with no key-name interpretation (tmux send-keys -l).
func (t *Terminal) Type(s string) error {
	return t.write([]byte(s))
}

// write mirrors input to the debug log, records that input was sent (which is
// what keeps WaitStable from reporting the pre-input screen as stable), then
// sends it to the child.
//
// Input to a child that has gone is refused with an error wrapping
// ErrChildExited. The terminal only records the exit once the PTY has reached
// end of file, which means nothing holds the other end open to read the bytes,
// so writing them anyway can only fail, and how it fails depends on the
// platform: macOS reports an I/O error, Linux may accept the bytes and drop
// them.
func (t *Terminal) write(b []byte) error {
	t.mirror(b)
	t.markInput()
	if err := t.exitedError(); err != nil {
		return err
	}
	// Answers the terminal owes the program go first: they were produced
	// before this input was, and a real terminal delivers them in that order.
	t.inputMu.Lock()
	if q := t.takeResponsesLocked(); len(q) > 0 {
		b = append(q, b...)
	}
	err := t.proc.Write(b)
	if err == nil {
		t.inBytes.Add(int64(len(b)))
	}
	t.inputMu.Unlock()
	// A write usually fails because the program has just exited: its end of
	// the PTY is closed, so macOS answers EIO, but the pump has not yet read
	// the end of file and recorded the exit. inputErr waits briefly for it so
	// the error says what happened instead of passing on the errno.
	return t.inputErr("write", err)
}

// exitedError returns an error wrapping ErrChildExited when the child has
// exited, and nil while it runs.
func (t *Terminal) exitedError() error {
	t.mu.Lock()
	code, exited := t.exitLocked()
	t.mu.Unlock()
	if !exited {
		return nil
	}
	if st, _ := t.ExitStatus(); st.Signaled {
		return fmt.Errorf("tuitest: cannot send input, the program was %s: %w", st, ErrChildExited)
	}
	return fmt.Errorf("tuitest: cannot send input, the program exited with code %d: %w", code, ErrChildExited)
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
// on it. Linux accepts writes to a master whose other end has closed and
// discards the bytes, so there no write fails until Close has released the PTY.
//
// Only caller input comes through here. The goroutine that writes queued
// emulator responses calls proc.Write directly and drops its errors, and the
// pump, which never writes, is what closes Done, so nothing that Done depends
// on ever waits for it.
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
//
// A program that enabled in-band resize notifications (mode 2048) is sent the
// report as part of the call, after the PTY has its new size, the way a real
// terminal sends it.
func (t *Terminal) Resize(cols, rows int) error {
	if err := checkSize("Resize", cols, rows); err != nil {
		return err
	}
	t.mu.Lock()
	t.emu.Resize(cols, rows)
	t.gen++
	if t.presented != nil {
		// A frame held for an open synchronized update has the old size. It
		// is cut or padded to the new one rather than replaced by the live
		// grid, which holds the unfinished frame the hold exists to hide.
		t.presented = t.presented.resized(cols, rows)
	}
	t.lastInput = time.Now()
	resp := t.emu.TakeResponses()
	t.mu.Unlock()
	if err := t.proc.Resize(cols, rows); err != nil {
		return t.inputErr("resize", err)
	}
	// The emulator produces the mode 2048 report during Resize. Before it was
	// queued here it waited for the next write from the program, which a
	// program that learns its size from the report has no reason to make. It
	// is queued after the PTY has its new size, so a program that reads the
	// size on receiving it sees the same one.
	t.mu.Lock()
	t.queueResponses(resp)
	t.mu.Unlock()
	return nil
}

// Progress reports how many bytes the child has written so far and when the
// most recent write landed. A caller that sends input and then sees neither
// counter move has evidence the program stopped responding.
func (t *Terminal) Progress() (bytes int64, last time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outBytes, t.lastWrite
}

// InputBytes reports how many bytes have been written to the child's input so
// far: everything sent with SendKeys, Type, Paste and SendMouse, and the answers
// the terminal gave to the program's own queries, which the program reads from
// the same stream.
//
// It is the input side of Progress, and it exists for the one question no
// quiet window can answer: whether the program has caught up with its input. A
// program that reports how many bytes it has read can be compared against this
// count, and once the two agree the program has seen everything, however long
// the machine took to schedule it. WaitStable can only guess, and a program
// starved of CPU for longer than the stabilize interval defeats the guess.
//
// It does not take the terminal's lock, so it is safe to call from inside a
// WaitFor condition.
func (t *Terminal) InputBytes() int64 {
	return t.inBytes.Load()
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
