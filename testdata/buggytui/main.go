// Command buggytui is a fixture TUI with deliberate, individually selectable
// bugs. It exists to prove the fuzzer finds real failures rather than only
// reporting on well-behaved programs: each -bug value reproduces one of the
// failure classes tuitest fuzz claims to detect, and the fuzzer's tests assert
// that it finds that class and minimises a tape that replays it.
//
// With -bug none the program is well behaved, which matters just as much: it is
// the control that proves the detectors do not fire on a correct program.
//
// Quitting is bound to Ctrl+C alone, deliberately. Binding it to 'q' would make
// the fixture exit almost immediately under fuzzing, because generated text and
// hostile byte bursts contain 'q' constantly, and every run would end before it
// explored anything.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
)

const (
	esc = "\x1b"

	altScreenOn  = esc + "[?1049h"
	altScreenOff = esc + "[?1049l"
	mouseOn      = esc + "[?1000h" + esc + "[?1002h" + esc + "[?1006h"
	mouseOff     = esc + "[?1006l" + esc + "[?1002l" + esc + "[?1000l"
	cursorHide   = esc + "[?25l"
	cursorShow   = esc + "[?25h"
	clearScreen  = esc + "[2J" + esc + "[H"

	// keyF5 is the escape sequence tuitest sends for F5.
	keyF5 = esc + "[15~"
	// ctrlC quits.
	ctrlC = 0x03
)

// Bug names, one per failure class the fuzzer detects.
const (
	bugNone = "none"
	// bugPanicOnKey panics when it receives F5: a plain crash on a specific key.
	bugPanicOnKey = "panic-on-key"
	// bugHangOnNarrow stops responding once resized to a single column, the
	// classic degenerate-size wedge.
	bugHangOnNarrow = "hang-on-narrow"
	// bugDirtyExit quits without leaving the alternate screen or disabling
	// mouse reporting, wrecking the user's shell.
	bugDirtyExit = "dirty-exit"
	// bugMangleUnicode types input into a text field through a fixed-size
	// buffer that cuts a multi-byte rune in half, so well-formed text comes
	// back with U+FFFD in it. This is the textbook shape of the bug: a buffer
	// sized in bytes and a decode step that assumes it holds whole runes.
	//
	// The buffer is cut at fixed offsets in the input stream, and the field
	// keeps what it has decoded, so whether the bug shows depends only on the
	// bytes sent. Cutting each read instead, as this fixture once did, made
	// the bug depend on how the kernel happened to split the input between
	// reads, which changes with machine load, and made the test that finds it
	// fail intermittently.
	bugMangleUnicode = "mangle-unicode"
	// bugLoseMarker drops the marker for good once F9 has been pressed, the
	// shape of a mode toggle that hides part of the interface and forgets to
	// restore it. It is the fixture for user-supplied invariants: the marker is
	// a property of the screen that a caller can assert and the fuzzer cannot
	// know about.
	//
	// The trigger is a key rather than a resize so the reproduction is
	// deterministic. A key produces a redraw on the same pass that handles it,
	// while a resize arrives as a signal and the redraw races the harness's
	// settle, which makes minimisation accept reductions that only reproduce
	// sometimes.
	bugLoseMarker = "lose-marker"
)

// marker prefixes every line the fixture draws, so that however far the screen
// has scrolled the top left cell holds it. That makes it assertable at any size
// large enough to hold a line at all; at one column by one row the fixture's
// own newlines scroll every line away and the screen is blank, which no marker
// can survive.
const marker = "*"

// mangleWindow is the size of the buffer bugMangleUnicode decodes input
// through. It is not a multiple of the width of any of the generator's
// multi-byte fragments, so a run of them lands mid-rune.
const mangleWindow = 8

// fieldRunes is how many runes bugMangleUnicode's text field holds. Once it is
// full it takes no more, so a replacement character that reached it stays on
// screen for the rest of the run.
const fieldRunes = 40

// keyF9 is the escape sequence tuitest sends for F9, which latches
// bugLoseMarker.
const keyF9 = esc + "[20~"

var bug = flag.String("bug", bugNone,
	"deliberate bug to exhibit: none, panic-on-key, hang-on-narrow, dirty-exit, "+
		"mangle-unicode, lose-marker")

// noMouse makes the fixture a program without mouse support: it never enables
// mouse reporting, and it ignores mouse reports that arrive anyway, drawing
// nothing for them, which is what such a program does with input it does not
// understand. It is not a bug. It is the control for the hang detector, which
// must not count input a real terminal would never have sent.
var noMouse = flag.Bool("no-mouse", false, "do not enable mouse reporting, and ignore mouse reports")

// sgrMouse matches one SGR mouse report.
var sgrMouse = regexp.MustCompile(`\x1b\[<\d+;\d+;\d+[Mm]`)

func main() {
	flag.Parse()

	in := os.Stdin
	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "buggytui: raw mode: %v\n", err)
		os.Exit(1)
	}
	// restore puts the terminal back exactly as a well-behaved TUI would. The
	// dirty-exit bug deliberately bypasses it.
	mouseModesOn, mouseModesOff := mouseOn, mouseOff
	if *noMouse {
		mouseModesOn, mouseModesOff = "", ""
	}
	restore := func() {
		_ = term.Restore(in.Fd(), state)
		emit(mouseModesOff + cursorShow + altScreenOff)
	}

	emit(altScreenOn + mouseModesOn + cursorHide)

	width, height := size(in)

	// Input and resize both arrive as channels so the loop can react to either
	// promptly, the way a real TUI's event loop does. Reading input on a
	// blocking call instead would leave a resize unhandled until the next
	// keystroke, which would make the hang bug depend on input ordering.
	input := make(chan []byte, 16)
	go func() {
		defer close(input)
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				input <- chunk
			}
			if err != nil {
				return
			}
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	draw(width, height, "ready")
	writeAck(width, height)

	for {
		// The narrow check happens here, at the top of the loop, rather than
		// only in the resize branch below. A select picks uniformly among ready
		// cases, so checking only on resize would let a keystroke that arrived
		// at the same time be handled first, produce a redraw, and make the
		// wedge look like a responsive program. Checking the real terminal size
		// every iteration makes the bug deterministic, which is what lets a
		// test assert on it.
		if *bug == bugHangOnNarrow {
			if w, _ := size(in); w <= 1 {
				// The bug: a single-column terminal wedges the program. It
				// stays alive, keeps its file descriptors open, and produces no
				// further output, which is exactly the hang signature.
				//
				// This sleeps rather than blocking on an empty select, because
				// an empty select would let the Go runtime notice every
				// goroutine is parked and panic with a deadlock error, turning
				// the hang into a crash and testing the wrong detector.
				for {
					time.Sleep(time.Hour)
				}
			}
		}

		select {
		case <-winch:
			width, height = size(in)
			draw(width, height, fmt.Sprintf("resized to %dx%d", width, height))
			writeAck(width, height)

		case chunk, ok := <-input:
			if !ok {
				restore()
				os.Exit(0)
			}
			inHandled += int64(len(chunk))

			if *bug == bugLoseMarker && watchF9.saw(chunk) {
				// The bug: the marker is gone for the rest of the run, and
				// nothing the user does brings it back.
				markerLost = true
			}

			if *bug == bugPanicOnKey && watchF5.saw(chunk) {
				// The bug: F5 panics. restore is never reached, so the process
				// dies with a non-zero status.
				panic("buggytui: unhandled key F5")
			}

			if *noMouse {
				chunk = sgrMouse.ReplaceAll(chunk, nil)
				if len(chunk) == 0 {
					writeAck(width, height)
					continue
				}
			}

			if containsQuit(chunk) {
				if *bug == bugDirtyExit {
					// The bug: exit without restoring anything. The alternate
					// screen stays active and mouse reporting stays on.
					os.Exit(0)
				}
				restore()
				os.Exit(0)
			}

			if *bug == bugMangleUnicode {
				typeInto(chunk)
			}
			draw(width, height, describe(chunk))
			writeAck(width, height)
		}
	}
}

// ack names a file the fixture rewrites after handling each event, so a test can
// tell exactly when the fixture has caught up with its input instead of
// guessing from a quiet window. See writeAck.
var ack = flag.String("ack", "", "file to rewrite with \"pid in out cols rows\" after each handled event")

// inHandled and outWritten are the byte counts writeAck reports: input the
// event loop has handled, and output written to the terminal.
var inHandled, outWritten int64

// emit writes to the terminal and counts what it wrote.
func emit(s string) {
	n, _ := os.Stdout.WriteString(s)
	outWritten += int64(n)
}

// writeAck records how far the fixture has got: its pid, so an acknowledgement
// left by an earlier run of the fixture is never mistaken for this one's, the
// input bytes handled and the output bytes written so far, and the size it last
// drew at. It is written after the output it describes, so a reader that has
// received that much output is looking at the frame the fixture drew for that
// much input. The rename makes the update atomic, so a reader never sees half
// of it.
func writeAck(width, height int) {
	if *ack == "" {
		return
	}
	tmp := *ack + ".tmp"
	body := fmt.Sprintf("%d %d %d %d %d", os.Getpid(), inHandled, outWritten, width, height)
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, *ack)
}

// keyWatch reports whether one key sequence has arrived, matching across read
// boundaries. A terminal read can split an escape sequence anywhere, so testing
// each chunk on its own would miss the key whenever the split lands inside it
// and make the bug it triggers look timing dependent when it is not.
//
// The search runs over the held tail and the whole chunk before anything is
// trimmed. Trimming first, as this once did, kept only the chunk's last few
// bytes, so a key followed by more input in the same read was never seen. The
// kernel coalesces input into fewer, larger reads when the program is slow to
// read, so the bug went quiet exactly when the machine was loaded.
type keyWatch struct {
	key  string
	tail []byte // the last few input bytes, one short of a whole sequence
}

func (w *keyWatch) saw(chunk []byte) bool {
	w.tail = append(w.tail, chunk...)
	found := strings.Contains(string(w.tail), w.key)
	// Keep one byte short of a whole sequence: enough to finish one that the
	// next read completes, never a whole one to be found twice.
	if keep := len(w.key) - 1; len(w.tail) > keep {
		w.tail = append([]byte(nil), w.tail[len(w.tail)-keep:]...)
	}
	return found
}

// watchF9 and watchF5 track the keys that trigger bugLoseMarker and
// bugPanicOnKey.
var (
	watchF9 = keyWatch{key: keyF9}
	watchF5 = keyWatch{key: keyF5}
)

// markerLost is bugLoseMarker's latch. Once set it is never cleared, which is
// what makes the bug a standing violation rather than a transient one and what
// gives the onset an unambiguous command to point at.
var markerLost bool

func size(f *os.File) (int, int) {
	w, h, err := term.GetSize(f.Fd())
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

func containsQuit(b []byte) bool {
	for _, c := range b {
		if c == ctrlC {
			return true
		}
	}
	return false
}

// describe renders input as a printable summary. It handles arbitrary bytes,
// including invalid UTF-8, on purpose: the fixture's own input path must not be
// the thing that breaks, or the tests would be measuring the wrong bug.
func describe(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == 0x1b:
			sb.WriteString("<ESC>")
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&sb, "<%02x>", c)
		default:
			sb.WriteByte(c)
		}
		if sb.Len() > 60 {
			break
		}
	}
	return sb.String()
}

// typeInto is bugMangleUnicode's input path: it decodes the input stream in
// mangleWindow-byte pieces, carrying the remainder of a read over to the next,
// and appends each piece's runes to the field. Ranging over a piece that ends
// mid-rune yields utf8.RuneError for the broken tail, and the field stores it,
// so what reaches the terminal is a well-formed U+FFFD rather than the raw
// bytes. That is the point: the invalid bytes never leave the program, so
// nothing on the wire looks wrong, and the only evidence is the character on
// screen. Control characters are left out, as a text field would.
func typeInto(b []byte) {
	pending = append(pending, b...)
	for len(pending) >= mangleWindow {
		for _, r := range string(pending[:mangleWindow]) {
			if len(field) >= fieldRunes {
				break
			}
			if r < 0x20 || r == 0x7f {
				continue
			}
			field = append(field, r)
		}
		pending = pending[mangleWindow:]
	}
}

// pending holds input bytes not yet decoded, and field the text decoded so far.
var (
	pending []byte
	field   []rune
)

// draw repaints the screen. Every event produces output, which is what gives
// the fuzzer's hang detector a baseline of responsiveness to measure against.
func draw(width, height int, status string) {
	// prefix marks a line, unless bugLoseMarker has latched. Every line carries
	// the marker so the invariant holds at any size and after any amount of
	// scrolling.
	prefix := marker
	if markerLost {
		prefix = ""
	}
	var sb strings.Builder
	sb.WriteString(clearScreen)
	writeLine(&sb, width, prefix+fmt.Sprintf("buggytui %dx%d", width, height))
	writeLine(&sb, width, prefix+"bug: "+*bug)
	writeLine(&sb, width, prefix+"last: "+status)
	writeLine(&sb, width, prefix+"ctrl-c to quit")
	lines := 4
	if *bug == bugMangleUnicode {
		writeLine(&sb, width, prefix+"text: "+string(field))
		lines++
	}
	// Fill the rest of the screen so a resize visibly changes the output.
	fill := prefix + strings.Repeat(".", min(max(width, 0), 40))
	for i := lines; i < height; i++ {
		writeLine(&sb, width, fill)
	}
	emit(sb.String())
}

// writeLine emits one row, truncated so it cannot exceed the terminal width.
// Without this a line that is one column too long soft-wraps, the screen scrolls
// by an extra row, and the character at the top left becomes whatever the wrap
// happened to put there. That is not a bug in the program under test, but an
// invariant reading the top left cell cannot tell the difference, so the fixture
// must not do it: a control that reports the fixture's own layout arithmetic
// measures nothing about the detectors.
func writeLine(sb *strings.Builder, width int, text string) {
	if width > 0 {
		r := []rune(text)
		if len(r) > width {
			text = string(r[:width])
		}
	}
	sb.WriteString(text + "\r\n")
}
