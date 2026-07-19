// Command tuitest tests terminal programs from the command line. It spawns a
// program in a real pseudo-terminal, interprets its output with a VT emulator,
// and asserts on the screen the program actually draws.
//
// Run "tuitest help" for the command list. Everything lives in internal/cli so
// the command surface, including exit codes and rendered error text, is
// testable without exec'ing the binary.
package main

import (
	"os"

	"github.com/Gaurav-Gosain/tuitest/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.OSEnv(), os.Args[1:]))
}
