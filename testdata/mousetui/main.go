// Command mousetui is a fixture for tuitest's own tests that behaves like a
// program which actually uses the input protocols: it turns on mouse reporting,
// bracketed paste and focus reporting, and it queries the terminal the way a
// real TUI does at startup.
//
// It reads raw bytes rather than lines and prints a one-line summary of each
// sequence it recognises, so a recording made against it can be checked both for
// what the tape says and for what the program actually received.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

const esc = "\x1b"

func main() {
	// Raw mode, so the protocol bytes reach this program instead of being
	// echoed and line buffered by the PTY's line discipline. A real TUI does
	// the same thing, and without it the fixture would only ever see whole
	// lines and would never observe a mouse report at all.
	if st, err := term.MakeRaw(os.Stdin.Fd()); err == nil {
		defer term.Restore(os.Stdin.Fd(), st) //nolint:errcheck
	}

	// Enable the protocols. 1002 reports drags, 1006 selects the SGR encoding,
	// 1004 focus events, 2004 bracketed paste.
	fmt.Print(esc + "[?1002h" + esc + "[?1006h" + esc + "[?1004h" + esc + "[?2004h")
	// Query the terminal, which is what makes replies arrive on the input
	// channel and is the situation that produced the reported corruption.
	fmt.Print(esc + "[c")
	fmt.Print(esc + "_Gi=1,a=q;\x1b\\")

	fmt.Print(esc + "[2J" + esc + "[H")
	fmt.Print("MOUSETUI ready\r\n")

	defer fmt.Print(esc + "[?1002l" + esc + "[?1006l" + esc + "[?1004l" + esc + "[?2004l")

	r := bufio.NewReader(os.Stdin)
	buf := make([]byte, 0, 256)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		buf = append(buf, b)

		// A sequence is complete once it ends in a plausible final byte; this
		// fixture only has to be good enough to report what it saw.
		s := string(buf)
		switch {
		case strings.HasPrefix(s, esc+"[<") && (b == 'M' || b == 'm'):
			fmt.Printf("mouse %s\r\n", strings.TrimPrefix(s, esc+"["))
		case s == esc+"[I":
			fmt.Print("focus in\r\n")
		case s == esc+"[O":
			fmt.Print("focus out\r\n")
		case strings.HasPrefix(s, esc+"[200~") && strings.HasSuffix(s, esc+"[201~"):
			body := strings.TrimSuffix(strings.TrimPrefix(s, esc+"[200~"), esc+"[201~")
			fmt.Printf("paste %q\r\n", body)
		case b == 'q' && len(buf) == 1:
			fmt.Print("bye\r\n")
			os.Exit(0)
		case b >= 0x20 && b < 0x7f && len(buf) == 1:
			fmt.Printf("key %c\r\n", b)
		// The answers to the startup queries above. A terminal that answers
		// them delivers the replies on this same input channel, so they have
		// to be consumed: left in the buffer, they prefix the next sequence
		// and stop it from being recognised at all.
		case strings.HasPrefix(s, esc+"[?") && b == 'c': // device attributes
		case strings.HasPrefix(s, esc+"_G") && strings.HasSuffix(s, esc+`\`): // kitty graphics
		default:
			continue
		}
		buf = buf[:0]
	}
}
