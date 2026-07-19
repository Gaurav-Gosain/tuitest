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
)

var bug = flag.String("bug", bugNone,
	"deliberate bug to exhibit: none, panic-on-key, hang-on-narrow, dirty-exit")

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
	restore := func() {
		_ = term.Restore(in.Fd(), state)
		fmt.Print(mouseOff + cursorShow + altScreenOff)
	}

	fmt.Print(altScreenOn + mouseOn + cursorHide)

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

		case chunk, ok := <-input:
			if !ok {
				restore()
				os.Exit(0)
			}

			if *bug == bugPanicOnKey && strings.Contains(string(chunk), keyF5) {
				// The bug: F5 panics. restore is never reached, so the process
				// dies with a non-zero status.
				panic("buggytui: unhandled key F5")
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

			draw(width, height, describe(chunk))
		}
	}
}

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

// draw repaints the screen. Every event produces output, which is what gives
// the fuzzer's hang detector a baseline of responsiveness to measure against.
func draw(width, height int, status string) {
	var sb strings.Builder
	sb.WriteString(clearScreen)
	fmt.Fprintf(&sb, "buggytui %dx%d\r\n", width, height)
	fmt.Fprintf(&sb, "bug: %s\r\n", *bug)
	fmt.Fprintf(&sb, "last: %s\r\n", status)
	sb.WriteString("ctrl-c to quit\r\n")
	// Fill the rest of the screen so a resize visibly changes the output.
	fill := strings.Repeat(".", min(max(width, 0), 40))
	for i := 4; i < height; i++ {
		sb.WriteString(fill + "\r\n")
	}
	fmt.Print(sb.String())
}
