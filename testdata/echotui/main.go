// Command echotui is a tiny deterministic terminal program used as a fixture
// for tuitest's own tests. It clears the screen, draws a bold banner, and echoes
// each line it reads back with a styled "echo:" prefix. Sending the line "quit"
// exits with status 0; "boom" exits with status 3. It relies on the PTY's
// default cooked line discipline, so typed input is line-buffered and
// tty-echoed.
package main

import (
	"bufio"
	"fmt"
	"os"
)

const (
	esc   = "\x1b"
	clear = esc + "[2J" + esc + "[H"
	bold  = esc + "[1m"
	green = esc + "[32m"
	reset = esc + "[0m"
)

func main() {
	fmt.Print(clear)
	fmt.Print(bold + "ECHOTUI" + reset + "\r\n")
	fmt.Print("> ")

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		switch line {
		case "quit":
			fmt.Print("\r\nbye\r\n")
			os.Exit(0)
		case "boom":
			os.Exit(3)
		}
		fmt.Printf("\r\n%secho:%s %s\r\n> ", green, reset, line)
	}
	os.Exit(0)
}
