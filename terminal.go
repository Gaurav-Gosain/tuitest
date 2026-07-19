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

// WithSize sets the initial PTY size in cells.
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
	closed    bool      // stream ended (child EOF)
	exited    bool
	exitCode  int

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
	t.proc = proc
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
	tb.Cleanup(func() { _ = term.Close() })
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
	t.mu.Unlock()
	t.mirror(p)
	// The pump is a single goroutine, so mirroring outside the lock still
	// delivers chunks to w in the order the child produced them.
	if t.outMirror != nil {
		_, _ = t.outMirror.Write(p)
	}
}

func (t *Terminal) onClose(code int) {
	t.mu.Lock()
	t.closed = true
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
	return &screenSnapshot{
		cols:       cols,
		rows:       rows,
		cells:      cells,
		curCol:     curCol,
		curRow:     curRow,
		curVisible: curVis,
		exitCode:   t.exitCode,
		exited:     t.exited,
	}
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
	return t.proc.Write(b)
}

// markInput timestamps the moment input was handed to the child.
func (t *Terminal) markInput() {
	t.mu.Lock()
	t.lastInput = time.Now()
	t.mu.Unlock()
}

// Resize changes the PTY window size and the emulator grid; the child receives
// SIGWINCH. Like sending keys, a resize counts as input for WaitStable, since
// the redraw it provokes has not arrived yet.
func (t *Terminal) Resize(cols, rows int) error {
	t.mu.Lock()
	t.emu.Resize(cols, rows)
	t.lastInput = time.Now()
	t.mu.Unlock()
	return t.proc.Resize(cols, rows)
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
// The process records its status before closing the channel Done reports on,
// whereas this terminal's own copy is filled in by the OnClose callback that
// runs after it. Consulting the process first closes that window, so a caller
// that reads the exit code the instant Done fires sees the real code rather
// than the not-yet-exited placeholder.
func (t *Terminal) ExitCode() (int, bool) {
	if t.proc != nil {
		if code, exited := t.proc.ExitCode(); exited {
			return code, true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, t.exited
}

// WaitExit blocks until the child exits or timeout elapses, returning the exit
// code. On timeout it returns -1 and a *TimeoutError, which unwraps to
// ErrTimeout like every other wait.
func (t *Terminal) WaitExit(timeout time.Duration) (int, error) {
	// Wait on the terminal's own view of the exit rather than on the process
	// handle. The pump closes the process's done channel before it delivers the
	// exit code here, so waking on that channel could observe the code before
	// this Terminal had recorded it and report -1 for a clean exit.
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
