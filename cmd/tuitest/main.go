// Command tuitest drives terminal programs under test from the command line.
//
//	tuitest run script.tape        replay a tape and check its assertions
//	tuitest fuzz -- program        hunt for crashes, hangs, and corruption
//
// It is a thin front-end over the tuitest Go API: everything it does is
// available to a Go test, and both subcommands drive the same tape player.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

func main() {
	os.Exit(run())
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tuitest <command> [flags] [arguments]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  run    replay a tape script against a program")
	fmt.Fprintln(os.Stderr, "  fuzz   drive a program with randomised input and report failures")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "run 'tuitest <command> -h' for the flags of a command")
}

func run() int {
	args := os.Args[1:]
	if len(args) < 1 {
		usage()
		return 2
	}

	switch args[0] {
	case "run":
		return runTape(args[1:])
	case "fuzz":
		return runFuzz(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tuitest: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

// runTape implements "tuitest run [flags] script.tape".
func runTape(args []string) int {
	fs := flag.NewFlagSet("tuitest run", flag.ContinueOnError)
	update := fs.Bool("update", false, "rewrite golden snapshots instead of comparing")
	strict := fs.Bool("strict", false, "treat Sleep as an error")
	goldenDir := fs.String("golden-dir", "", "directory for golden files (default: ./testdata)")
	verbose := fs.Bool("v", false, "mirror PTY I/O to stderr")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tuitest run [flags] script.tape")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	tapePath := fs.Arg(0)
	f, err := os.Open(tapePath) //nolint:gosec
	if err != nil {
		fmt.Fprintf(os.Stderr, "tuitest: %v\n", err)
		return 1
	}
	defer f.Close() //nolint:errcheck

	cmds, err := tape.Parse(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tuitest: parse %s: %v\n", tapePath, err)
		return 1
	}

	p := tape.NewPlayer()
	if *goldenDir != "" {
		p.GoldenDir = *goldenDir
	}
	if *update {
		p.Update = true
	}
	p.Strict = *strict
	if *verbose {
		p.Out = os.Stderr
	}

	if err := p.Run(cmds); err != nil {
		fmt.Fprintf(os.Stderr, "tuitest: %v\n", err)
		return 1
	}
	return 0
}
