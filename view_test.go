package tuitest

import (
	"os/exec"
	"testing"
	"time"
)

const (
	bsu = "\x1b[?2026h" // begin synchronized update
	esu = "\x1b[?2026l" // end synchronized update
)

// TestScreenHoldsTheLastFrameDuringASynchronizedUpdate is the harness side of
// DEC mode 2026. A program that wraps each frame in a synchronized update never
// wants the half-drawn frame shown, and a real terminal does not show it. The
// PTY delivers the frame in pieces, so before this the screen a wait or a read
// saw could be the top of the new frame over the bottom of the old one.
func TestScreenHoldsTheLastFrameDuringASynchronizedUpdate(t *testing.T) {
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("\x1b[Hold top\r\nold bottom"))

	// The new frame arrives in two reads, split between its rows.
	term.onData([]byte(bsu + "\x1b[H\x1b[2Jnew top\r\n"))
	for _, got := range []string{term.Snapshot(), term.Screen().Text()} {
		if got != "old top\nold bottom" {
			t.Fatalf("mid-update the screen should still be the old frame, got:\n%s", got)
		}
	}
	if err := term.WaitForText("new top", 50*time.Millisecond); err == nil {
		t.Fatal("WaitForText matched text from a frame that was still being drawn")
	}

	term.onData([]byte("new bottom" + esu))
	if got := term.Snapshot(); got != "new top\nnew bottom" {
		t.Fatalf("after the update closed the screen should be the new frame, got:\n%s", got)
	}
}

// TestSynchronizedUpdateBeginningMidChunk checks that the held frame includes
// whatever the same read wrote before the update opened.
func TestSynchronizedUpdateBeginningMidChunk(t *testing.T) {
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("before" + bsu + "\x1b[2J\x1b[Hduring"))
	if got := term.Snapshot(); got != "before" {
		t.Fatalf("the held frame should be the grid as the update opened, got %q", got)
	}
	term.onData([]byte(esu))
	if got := term.Snapshot(); got != "during" {
		t.Fatalf("got %q after the update closed", got)
	}
}

// TestSynchronizedUpdateIsNotHeldForever covers a program that opens an update
// and never closes it: the live grid is shown once the hold limit passes, and at
// once when the program exits.
func TestSynchronizedUpdateIsNotHeldForever(t *testing.T) {
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("old" + bsu + "\x1b[2J\x1b[Hnew"))
	if got := term.Snapshot(); got != "old" {
		t.Fatalf("got %q, want the held frame", got)
	}

	term.mu.Lock()
	term.syncSince = time.Now().Add(-syncHoldLimit)
	term.mu.Unlock()
	if got := term.Snapshot(); got != "new" {
		t.Fatalf("after the hold limit the live grid should show, got %q", got)
	}

	term2 := newIdleTerminal(DefaultStabilizeInterval)
	term2.onData([]byte("old" + bsu + "\x1b[2J\x1b[Hlast words"))
	term2.onClose(0)
	if got := term2.Snapshot(); got != "last words" {
		t.Fatalf("after exit the live grid should show, got %q", got)
	}
}

// TestResizeDuringASynchronizedUpdateReportsTheNewSize keeps Size honest when
// a resize lands while a frame is held, without showing the unfinished frame.
func TestResizeDuringASynchronizedUpdateReportsTheNewSize(t *testing.T) {
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}
	term, err := Start([]string{cat}, WithSize(20, 5))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })

	// cat writes nothing on its own, so this is the only output.
	term.onData([]byte("old" + bsu + "new"))
	if err := term.Resize(30, 8); err != nil {
		t.Fatal(err)
	}
	s := term.Screen()
	if cols, rows := s.Size(); cols != 30 || rows != 8 {
		t.Fatalf("Size() = %dx%d, want 30x8", cols, rows)
	}
	if got := s.Text(); got != "old" {
		t.Fatalf("the held frame should survive the resize, got %q", got)
	}
}

func TestResizedSnapshotCutsAndPads(t *testing.T) {
	const cjk = "\xe4\xb8\x96" // U+4E16, two cells wide
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("abc" + cjk + "\r\nxy"))
	term.mu.Lock()
	s := term.snapshotLocked()
	term.mu.Unlock()

	cut := s.resized(4, 1) // the wide rune starts in column 3 and cannot fit
	if cols, rows := cut.Size(); cols != 4 || rows != 1 {
		t.Fatalf("Size() = %dx%d, want 4x1", cols, rows)
	}
	if got := cut.Text(); got != "abc" {
		t.Errorf("cut Text() = %q, want %q", got, "abc")
	}
	grown := s.resized(30, 8)
	if got := grown.Text(); got != "abc"+cjk+"\nxy" {
		t.Errorf("padded Text() = %q", got)
	}
	if c := grown.Cell(29, 7); c.Content != " " || c.Width != 1 {
		t.Errorf("a padding cell should be a blank, got %+v", c)
	}
}

// TestScreenIsReusedUntilTheGridChanges pins the snapshot cache: an unchanged
// screen is handed out again rather than rebuilt, and any output, a resize or
// an exit produces a fresh one.
func TestScreenIsReusedUntilTheGridChanges(t *testing.T) {
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("one"))
	a, b := term.Screen(), term.Screen()
	if a != b {
		t.Fatal("two reads of an unchanged screen built two snapshots")
	}
	term.onData([]byte(" two"))
	c := term.Screen()
	if c == a {
		t.Fatal("the screen was not rebuilt after output")
	}
	if got := c.Text(); got != "one two" {
		t.Fatalf("Text() = %q, want %q", got, "one two")
	}
	term.onClose(3)
	d := term.Screen()
	if code, exited := d.ExitCode(); !exited || code != 3 {
		t.Fatalf("after exit Screen().ExitCode() = (%d, %v), want (3, true)", code, exited)
	}
	if a.Text() != "one" {
		t.Fatalf("an earlier snapshot changed after the fact: %q", a.Text())
	}
}

// TestWaitStableIgnoresASilentProgram pins the first-output rule: before the
// program has written anything, the screen is blank because nothing has been
// drawn yet, not because drawing is finished.
func TestWaitStableIgnoresASilentProgram(t *testing.T) {
	const quiet = 20 * time.Millisecond
	term := newTerminal(config{cols: 20, rows: 5, stabilize: quiet})
	term.mu.Lock()
	term.lastWrite = time.Now().Add(-time.Second) // spawned long ago
	term.mu.Unlock()

	if err := term.WaitStable(100 * time.Millisecond); err == nil {
		t.Fatal("WaitStable reported a program that has written nothing as stable")
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		term.onData([]byte("hello"))
	}()
	if err := term.WaitStable(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := term.Snapshot(); got != "hello" {
		t.Fatalf("WaitStable returned before the first output: %q", got)
	}
}

// TestWaitForPromptAfterAClearScreen drives the counter WaitForPrompt reads
// through the case that broke it: a shell's `clear` removed the markers the
// count was taken from, so the wait for the next prompt never ended.
func TestWaitForPromptAfterAClearScreen(t *testing.T) {
	term := newTerminal(config{cols: 20, rows: 5, semantic: true})
	term.onData([]byte("\x1b]133;A\x07$ clear\r\n"))
	go func() {
		time.Sleep(20 * time.Millisecond)
		term.onData([]byte("\x1b[H\x1b[2J\x1b]133;A\x07$ "))
	}()
	if err := term.WaitForPrompt(2 * time.Second); err != nil {
		t.Fatalf("WaitForPrompt after clear: %v", err)
	}
}

// TestTermStateSeesTheOriginalAltScreen covers a program that enters the
// alternate screen with mode 47, as older terminfo entries do, and exits
// without leaving it. TermState checked for 47 but the emulator never reported
// it, so the program was called clean.
func TestTermStateSeesTheOriginalAltScreen(t *testing.T) {
	term := newIdleTerminal(DefaultStabilizeInterval)
	term.onData([]byte("\x1b[?47h\x1b[?1h"))
	st := term.TermState()
	if !st.AltScreen || !st.Dirty() {
		t.Fatalf("TermState() = %+v, want the alternate screen reported", st)
	}
	if !st.Mode(47) || !st.Mode(1) {
		t.Errorf("Mode(47) = %v, Mode(1) = %v, want both set", st.Mode(47), st.Mode(1))
	}
}
