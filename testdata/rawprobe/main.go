// Command rawprobe is a fixture for tuitest's own tests that reports exactly
// which bytes it received. It puts the terminal in raw mode, writes its first
// argument verbatim (so a test can turn modes on the way a real program would),
// prints "READY", and then keeps one line up to date with every input byte so
// far in hex:
//
//	in: 1b 4f 41
//
// The third line holds the window size the kernel reports, in cells and in
// pixels, read again on each update:
//
//	size: 80x24 800x480
//
// Everything a test sends, and everything the terminal answers on the program's
// behalf, ends up on that line, so an assertion on it is an assertion on the
// wire.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// size reports the window size as the kernel has it.
func size() string {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return "size: " + err.Error()
	}
	return fmt.Sprintf("size: %dx%d %dx%d", ws.Col, ws.Row, ws.Xpixel, ws.Ypixel)
}

func main() {
	if st, err := term.MakeRaw(os.Stdin.Fd()); err == nil {
		defer term.Restore(os.Stdin.Fd(), st) //nolint:errcheck
	}
	if len(os.Args) > 1 {
		fmt.Print(os.Args[1])
	}
	fmt.Print("\x1b[2J\x1b[HREADY\r\n\r\n" + size())

	var seen []string
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		for _, b := range buf[:n] {
			seen = append(seen, fmt.Sprintf("%02x", b))
		}
		// Row 2, cleared and rewritten, so the line always holds the whole
		// history and the screen never scrolls it away.
		fmt.Printf("\x1b[2;1H\x1b[Jin: %s\r\n%s", strings.Join(seen, " "), size())
		if err != nil {
			return
		}
	}
}
