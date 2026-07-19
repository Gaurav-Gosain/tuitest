// Command tuitest drives terminal programs from tape scripts. It is a thin
// front-end over the tuitest Go API:
//
//	tuitest run script.tape       play a tape headlessly and assert on it
//	tuitest record -o s.tape prog interact with prog and write what you did
//	tuitest replay script.tape    play a tape onto your terminal to watch it
//
// Together record and replay close the loop: record produces a tape from a real
// session, run turns it into a test, and replay shows you the frame where that
// test went wrong.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	os.Exit(run())
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: tuitest <command> [flags] [args]

commands:
  run     play a tape and assert on it
  record  record an interactive session into a tape
  replay  play a tape onto this terminal, rendering as it goes

run "tuitest <command> -h" for the flags of a command
`)
}

func run() int {
	args := os.Args[1:]
	if len(args) < 1 {
		usage()
		return 2
	}

	switch args[0] {
	case "run":
		return runCmd(args[1:])
	case "record":
		return recordCmd(args[1:])
	case "replay":
		return replayCmd(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tuitest: unknown subcommand %q\n", args[0])
		usage()
		return 2
	}
}

// newFlagSet builds a flag set that prints the given usage line.
func newFlagSet(name, usageLine string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, usageLine)
		fs.PrintDefaults()
	}
	return fs
}

func errf(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "tuitest: "+format+"\n", args...)
	return 1
}
