// Command tuitest runs tape scripts against terminal programs under test. It is
// a thin front-end over the tuitest Go API: tuitest run script.tape parses the
// tape and drives a tuitest.Terminal, and -update rewrites golden snapshots.
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

func run() int {
	fs := flag.NewFlagSet("tuitest", flag.ContinueOnError)
	update := fs.Bool("update", false, "rewrite golden snapshots instead of comparing")
	strict := fs.Bool("strict", false, "treat Sleep as an error")
	goldenDir := fs.String("golden-dir", "", "directory for golden files (default: next to the tape)")
	verbose := fs.Bool("v", false, "mirror PTY I/O to stderr")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tuitest run [flags] script.tape")
		fs.PrintDefaults()
	}

	args := os.Args[1:]
	if len(args) < 1 {
		fs.Usage()
		return 2
	}
	sub := args[0]
	if sub != "run" {
		fmt.Fprintf(os.Stderr, "tuitest: unknown subcommand %q\n", sub)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
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
