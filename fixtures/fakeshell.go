// Package fixtures provides in-process helpers for testing code that consumes
// terminal output: FakeShell, an io.ReadWriteCloser that stands in for the
// shell side of a PTY, and ANSIBuilder, which assembles escape sequences
// without hand-written byte strings.
//
// Nothing here spawns a process. Use it to unit test a parser, an emulator or a
// renderer; use the root tuitest package to test a real program through a real
// PTY. It started as a copy of tuios's internal/testutil and has since been
// fixed independently: FakeShell no longer delivers queued output twice.
//
//	sh := fixtures.NewFakeShell()
//	sh.SendOutput(fixtures.NewANSIBuilder().Bold().Text("hi").Reset().String())
//	buf := make([]byte, 64)
//	n, _ := sh.Read(buf) // "\x1b[1mhi\x1b[0m"
package fixtures

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// FakeShell provides a controllable shell-like interface for testing.
// It can:
// - Send predictable output (text and ANSI sequences)
// - Receive and record input from the terminal
// - Simulate shell behavior for testing VT emulation
type FakeShell struct {
	mu           sync.Mutex
	inputBuf     *bytes.Buffer // Input received from terminal
	outputBuf    *bytes.Buffer // Output to send to terminal
	inputHistory []string      // History of all received input
	closed       bool
	closeOnce    sync.Once
	// outputBuf is the only copy of pending output. ready holds one token
	// whenever output may have arrived since a reader last looked, and done
	// is closed by Close, so a blocked reader wakes for either.
	ready chan struct{}
	done  chan struct{}
}

// NewFakeShell creates a new fake shell for testing.
func NewFakeShell() *FakeShell {
	return &FakeShell{
		inputBuf:     new(bytes.Buffer),
		outputBuf:    new(bytes.Buffer),
		inputHistory: make([]string, 0),
		ready:        make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
}

// Read implements io.Reader, reading output that should go to the terminal.
// This simulates PTY read (output from shell). It blocks until output is
// queued or the shell is closed, and returns io.EOF once it is closed.
func (f *FakeShell) Read(p []byte) (n int, err error) {
	return f.read(p, nil)
}

// read returns queued output, waiting for some if none is queued. A nil
// timeout channel waits forever.
func (f *FakeShell) read(p []byte, timeout <-chan time.Time) (int, error) {
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, io.EOF
		}
		if f.outputBuf.Len() > 0 {
			n, err := f.outputBuf.Read(p)
			f.mu.Unlock()
			return n, err
		}
		f.mu.Unlock()

		select {
		case <-f.ready:
		case <-f.done:
		case <-timeout:
			return 0, errReadTimeout
		}
	}
}

// errReadTimeout is returned by ReadWithTimeout when no output arrives in time.
var errReadTimeout = errors.New("fixtures: read timed out")

// notify wakes a blocked reader. The channel holds at most one token, which is
// all a reader needs: it rechecks the buffer after waking.
func (f *FakeShell) notify() {
	select {
	case f.ready <- struct{}{}:
	default:
	}
}

// Write implements io.Writer, receiving input from the terminal.
// This simulates PTY write (input to shell).
func (f *FakeShell) Write(p []byte) (n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return 0, io.EOF
	}

	n, err = f.inputBuf.Write(p)
	if n > 0 {
		f.inputHistory = append(f.inputHistory, string(p[:n]))
	}
	return n, err
}

// Close closes the fake shell.
func (f *FakeShell) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed = true
		close(f.done)
	})
	return nil
}

// SendOutput queues output to be read by the terminal.
// Use this to simulate shell output. It does nothing after Close.
func (f *FakeShell) SendOutput(data string) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.outputBuf.WriteString(data)
	f.mu.Unlock()
	f.notify()
}

// SendBytes queues raw bytes to be read by the terminal. The bytes are copied,
// so the caller may reuse data. It does nothing after Close.
func (f *FakeShell) SendBytes(data []byte) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.outputBuf.Write(data)
	f.mu.Unlock()
	f.notify()
}

// GetInput returns all input received so far.
func (f *FakeShell) GetInput() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputBuf.String()
}

// GetInputHistory returns a copy of all input chunks received.
func (f *FakeShell) GetInputHistory() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, len(f.inputHistory))
	copy(result, f.inputHistory)
	return result
}

// ClearInput clears the input buffer and history.
func (f *FakeShell) ClearInput() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputBuf.Reset()
	f.inputHistory = f.inputHistory[:0]
}

// IsClosed returns whether the shell has been closed.
func (f *FakeShell) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// SendOutputf queues formatted output to be read by the terminal.
func (f *FakeShell) SendOutputf(format string, args ...any) {
	f.SendOutput(fmt.Sprintf(format, args...))
}

// ReadWithTimeout is Read with a deadline. It returns io.EOF once the shell is
// closed, and a non-nil error other than io.EOF if no output arrives within
// timeout.
func (f *FakeShell) ReadWithTimeout(p []byte, timeout time.Duration) (int, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	n, err := f.read(p, timer.C)
	if errors.Is(err, errReadTimeout) {
		return n, fmt.Errorf("read timed out after %v: %w", timeout, err)
	}
	return n, err
}

// =============================================================================
// ANSI Escape Sequence Helpers
// =============================================================================

// ANSI escape sequence constants
const (
	ESC = "\x1b"
	CSI = ESC + "["
	OSC = ESC + "]"
	DCS = ESC + "P"
	APC = ESC + "_"
	ST  = ESC + "\\" // String Terminator
	BEL = "\x07"     // Bell (also terminates OSC)
)

// ANSIBuilder helps construct ANSI escape sequences.
type ANSIBuilder struct {
	buf strings.Builder
}

// NewANSIBuilder creates a new ANSI builder.
func NewANSIBuilder() *ANSIBuilder {
	return &ANSIBuilder{}
}

// Text appends plain text.
func (a *ANSIBuilder) Text(s string) *ANSIBuilder {
	a.buf.WriteString(s)
	return a
}

// Newline appends a newline.
func (a *ANSIBuilder) Newline() *ANSIBuilder {
	a.buf.WriteString("\r\n")
	return a
}

// CR appends a carriage return.
func (a *ANSIBuilder) CR() *ANSIBuilder {
	a.buf.WriteString("\r")
	return a
}

// LF appends a line feed.
func (a *ANSIBuilder) LF() *ANSIBuilder {
	a.buf.WriteString("\n")
	return a
}

// CursorTo moves cursor to position (1-based).
func (a *ANSIBuilder) CursorTo(row, col int) *ANSIBuilder {
	fmt.Fprintf(&a.buf, "%s%d;%dH", CSI, row, col)
	return a
}

// CursorHome moves cursor to home position (1,1).
func (a *ANSIBuilder) CursorHome() *ANSIBuilder {
	a.buf.WriteString(CSI + "H")
	return a
}

// CursorUp moves cursor up n lines.
func (a *ANSIBuilder) CursorUp(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "A")
	} else {
		fmt.Fprintf(&a.buf, "%s%dA", CSI, n)
	}
	return a
}

// CursorDown moves cursor down n lines.
func (a *ANSIBuilder) CursorDown(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "B")
	} else {
		fmt.Fprintf(&a.buf, "%s%dB", CSI, n)
	}
	return a
}

// CursorForward moves cursor forward n columns.
func (a *ANSIBuilder) CursorForward(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "C")
	} else {
		fmt.Fprintf(&a.buf, "%s%dC", CSI, n)
	}
	return a
}

// CursorBackward moves cursor backward n columns.
func (a *ANSIBuilder) CursorBackward(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "D")
	} else {
		fmt.Fprintf(&a.buf, "%s%dD", CSI, n)
	}
	return a
}

// ClearScreen clears the entire screen.
func (a *ANSIBuilder) ClearScreen() *ANSIBuilder {
	a.buf.WriteString(CSI + "2J")
	return a
}

// ClearLine clears the entire current line.
func (a *ANSIBuilder) ClearLine() *ANSIBuilder {
	a.buf.WriteString(CSI + "2K")
	return a
}

// ClearToEndOfLine clears from cursor to end of line.
func (a *ANSIBuilder) ClearToEndOfLine() *ANSIBuilder {
	a.buf.WriteString(CSI + "K")
	return a
}

// ClearToEndOfScreen clears from cursor to end of screen.
func (a *ANSIBuilder) ClearToEndOfScreen() *ANSIBuilder {
	a.buf.WriteString(CSI + "J")
	return a
}

// SGR sends a Select Graphic Rendition sequence.
func (a *ANSIBuilder) SGR(params ...int) *ANSIBuilder {
	if len(params) == 0 {
		a.buf.WriteString(CSI + "m")
		return a
	}

	a.buf.WriteString(CSI)
	for i, p := range params {
		if i > 0 {
			a.buf.WriteString(";")
		}
		fmt.Fprintf(&a.buf, "%d", p)
	}
	a.buf.WriteString("m")
	return a
}

// Reset resets all attributes.
func (a *ANSIBuilder) Reset() *ANSIBuilder {
	return a.SGR(0)
}

// Bold enables bold.
func (a *ANSIBuilder) Bold() *ANSIBuilder {
	return a.SGR(1)
}

// Dim enables dim/faint.
func (a *ANSIBuilder) Dim() *ANSIBuilder {
	return a.SGR(2)
}

// Italic enables italic.
func (a *ANSIBuilder) Italic() *ANSIBuilder {
	return a.SGR(3)
}

// Underline enables underline.
func (a *ANSIBuilder) Underline() *ANSIBuilder {
	return a.SGR(4)
}

// Blink enables blink.
func (a *ANSIBuilder) Blink() *ANSIBuilder {
	return a.SGR(5)
}

// Reverse enables reverse video.
func (a *ANSIBuilder) Reverse() *ANSIBuilder {
	return a.SGR(7)
}

// Hidden enables hidden text.
func (a *ANSIBuilder) Hidden() *ANSIBuilder {
	return a.SGR(8)
}

// Strikethrough enables strikethrough.
func (a *ANSIBuilder) Strikethrough() *ANSIBuilder {
	return a.SGR(9)
}

// FgColor sets foreground to a basic color (30-37, 90-97).
func (a *ANSIBuilder) FgColor(color int) *ANSIBuilder {
	return a.SGR(color)
}

// BgColor sets background to a basic color (40-47, 100-107).
func (a *ANSIBuilder) BgColor(color int) *ANSIBuilder {
	return a.SGR(color)
}

// Fg256 sets foreground to a 256-color palette color.
func (a *ANSIBuilder) Fg256(color int) *ANSIBuilder {
	return a.SGR(38, 5, color)
}

// Bg256 sets background to a 256-color palette color.
func (a *ANSIBuilder) Bg256(color int) *ANSIBuilder {
	return a.SGR(48, 5, color)
}

// FgRGB sets foreground to an RGB color.
func (a *ANSIBuilder) FgRGB(r, g, b int) *ANSIBuilder {
	return a.SGR(38, 2, r, g, b)
}

// BgRGB sets background to an RGB color.
func (a *ANSIBuilder) BgRGB(r, g, b int) *ANSIBuilder {
	return a.SGR(48, 2, r, g, b)
}

// SaveCursor saves cursor position.
func (a *ANSIBuilder) SaveCursor() *ANSIBuilder {
	a.buf.WriteString(CSI + "s")
	return a
}

// RestoreCursor restores cursor position.
func (a *ANSIBuilder) RestoreCursor() *ANSIBuilder {
	a.buf.WriteString(CSI + "u")
	return a
}

// ShowCursor shows the cursor.
func (a *ANSIBuilder) ShowCursor() *ANSIBuilder {
	a.buf.WriteString(CSI + "?25h")
	return a
}

// HideCursor hides the cursor.
func (a *ANSIBuilder) HideCursor() *ANSIBuilder {
	a.buf.WriteString(CSI + "?25l")
	return a
}

// AltScreen switches to alternate screen buffer.
func (a *ANSIBuilder) AltScreen() *ANSIBuilder {
	a.buf.WriteString(CSI + "?1049h")
	return a
}

// MainScreen switches back to main screen buffer.
func (a *ANSIBuilder) MainScreen() *ANSIBuilder {
	a.buf.WriteString(CSI + "?1049l")
	return a
}

// EnableBracketedPaste enables bracketed paste mode.
func (a *ANSIBuilder) EnableBracketedPaste() *ANSIBuilder {
	a.buf.WriteString(CSI + "?2004h")
	return a
}

// DisableBracketedPaste disables bracketed paste mode.
func (a *ANSIBuilder) DisableBracketedPaste() *ANSIBuilder {
	a.buf.WriteString(CSI + "?2004l")
	return a
}

// EnableMouse enables mouse tracking.
func (a *ANSIBuilder) EnableMouse() *ANSIBuilder {
	a.buf.WriteString(CSI + "?1000h")
	return a
}

// DisableMouse disables mouse tracking.
func (a *ANSIBuilder) DisableMouse() *ANSIBuilder {
	a.buf.WriteString(CSI + "?1000l")
	return a
}

// EnableSGRMouse enables SGR mouse mode.
func (a *ANSIBuilder) EnableSGRMouse() *ANSIBuilder {
	a.buf.WriteString(CSI + "?1006h")
	return a
}

// DisableSGRMouse disables SGR mouse mode.
func (a *ANSIBuilder) DisableSGRMouse() *ANSIBuilder {
	a.buf.WriteString(CSI + "?1006l")
	return a
}

// ScrollRegion sets the scroll region.
func (a *ANSIBuilder) ScrollRegion(top, bottom int) *ANSIBuilder {
	fmt.Fprintf(&a.buf, "%s%d;%dr", CSI, top, bottom)
	return a
}

// ScrollUp scrolls up n lines.
func (a *ANSIBuilder) ScrollUp(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "S")
	} else {
		fmt.Fprintf(&a.buf, "%s%dS", CSI, n)
	}
	return a
}

// ScrollDown scrolls down n lines.
func (a *ANSIBuilder) ScrollDown(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "T")
	} else {
		fmt.Fprintf(&a.buf, "%s%dT", CSI, n)
	}
	return a
}

// InsertLines inserts n blank lines.
func (a *ANSIBuilder) InsertLines(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "L")
	} else {
		fmt.Fprintf(&a.buf, "%s%dL", CSI, n)
	}
	return a
}

// DeleteLines deletes n lines.
func (a *ANSIBuilder) DeleteLines(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "M")
	} else {
		fmt.Fprintf(&a.buf, "%s%dM", CSI, n)
	}
	return a
}

// InsertChars inserts n blank characters.
func (a *ANSIBuilder) InsertChars(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "@")
	} else {
		fmt.Fprintf(&a.buf, "%s%d@", CSI, n)
	}
	return a
}

// DeleteChars deletes n characters.
func (a *ANSIBuilder) DeleteChars(n int) *ANSIBuilder {
	if n == 1 {
		a.buf.WriteString(CSI + "P")
	} else {
		fmt.Fprintf(&a.buf, "%s%dP", CSI, n)
	}
	return a
}

// OSCTitle sets the window title.
func (a *ANSIBuilder) OSCTitle(title string) *ANSIBuilder {
	a.buf.WriteString(OSC + "0;" + title + BEL)
	return a
}

// OSCHyperlink creates a hyperlink.
func (a *ANSIBuilder) OSCHyperlink(url, text string) *ANSIBuilder {
	a.buf.WriteString(OSC + "8;;" + url + ST + text + OSC + "8;;" + ST)
	return a
}

// DeviceStatusReport requests cursor position (DSR).
func (a *ANSIBuilder) DeviceStatusReport() *ANSIBuilder {
	a.buf.WriteString(CSI + "6n")
	return a
}

// RequestTerminalSize requests terminal size (XTWINOPS).
func (a *ANSIBuilder) RequestTerminalSize() *ANSIBuilder {
	a.buf.WriteString(CSI + "18t")
	return a
}

// Raw appends raw bytes.
func (a *ANSIBuilder) Raw(data []byte) *ANSIBuilder {
	a.buf.Write(data)
	return a
}

// RawString appends a raw string.
func (a *ANSIBuilder) RawString(s string) *ANSIBuilder {
	a.buf.WriteString(s)
	return a
}

// String returns the built string.
func (a *ANSIBuilder) String() string {
	return a.buf.String()
}

// Bytes returns the built bytes.
func (a *ANSIBuilder) Bytes() []byte {
	return []byte(a.buf.String())
}

// Clear empties the builder. Reset, by contrast, appends SGR 0.
func (a *ANSIBuilder) Clear() *ANSIBuilder {
	a.buf.Reset()
	return a
}

// =============================================================================
// Common Test Patterns
// =============================================================================

// ShellPrompt returns a typical shell prompt sequence.
func ShellPrompt(user, host, dir string) string {
	return NewANSIBuilder().
		FgColor(32). // Green
		Text(user + "@" + host).
		Reset().
		Text(":").
		FgColor(34). // Blue
		Text(dir).
		Reset().
		Text("$ ").
		String()
}

// ColoredLine returns a line with colored text.
func ColoredLine(color int, text string) string {
	return NewANSIBuilder().
		FgColor(color).
		Text(text).
		Reset().
		Newline().
		String()
}

// LSOutput simulates `ls` command output with colors. isDir[i] says whether
// files[i] is a directory, so it must be at least as long as files.
func LSOutput(files []string, isDir []bool) string {
	b := NewANSIBuilder()
	for i, file := range files {
		if isDir[i] {
			b.FgColor(34).Bold().Text(file).Reset().Text("  ")
		} else {
			b.Text(file).Text("  ")
		}
	}
	return b.Newline().String()
}

// ProgressBar simulates a progress bar update. percent is clamped to 0..100,
// so the bar is always width cells between its brackets.
func ProgressBar(percent int, width int) string {
	percent = min(max(percent, 0), 100)
	filled := width * percent / 100
	empty := width - filled

	b := NewANSIBuilder().
		CR(). // Return to start of line
		Text("[")

	for range filled {
		b.Text("=")
	}
	if filled < width {
		b.Text(">")
		for range empty - 1 {
			b.Text(" ")
		}
	}

	return b.Text(fmt.Sprintf("] %3d%%", percent)).String()
}

// SpinnerFrame returns a single frame of a spinner animation.
func SpinnerFrame(frame int) string {
	frames := []string{"|", "/", "-", "\\"}
	return NewANSIBuilder().
		CR().
		Text(frames[frame%len(frames)]).
		Text(" Loading...").
		String()
}

// CursorPositionResponse returns the response to a DSR query.
func CursorPositionResponse(row, col int) string {
	return fmt.Sprintf("%s%d;%dR", CSI, row, col)
}

// TerminalSizeResponse returns the response to an XTWINOPS size query.
func TerminalSizeResponse(rows, cols int) string {
	return fmt.Sprintf("%s8;%d;%dt", CSI, rows, cols)
}

// ErrorOutput returns a bash-style error message: "bash: cmd: msg".
func ErrorOutput(cmd, msg string) string {
	return fmt.Sprintf("bash: %s: %s\n", cmd, msg)
}

// CommandNotFound returns a bash-style "command not found" error.
func CommandNotFound(cmd string) string {
	return ErrorOutput(cmd, "command not found")
}

// TabCompletionResponse returns a tab completion display with the given options.
func TabCompletionResponse(options []string) string {
	return strings.Join(options, "  ") + "\r\n"
}
