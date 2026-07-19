package main

import (
	"os"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

func runCmd(args []string) int {
	fs := newFlagSet("run", "usage: tuitest run [flags] script.tape")
	update := fs.Bool("update", false, "rewrite golden snapshots instead of comparing")
	strict := fs.Bool("strict", false, "treat Sleep as an error")
	goldenDir := fs.String("golden-dir", "", "directory for golden files (default: ./testdata)")
	verbose := fs.Bool("v", false, "mirror PTY I/O to stderr")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	cmds, err := parseTapeFile(fs.Arg(0))
	if err != nil {
		return errf("%v", err)
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
		return errf("%v", err)
	}
	return 0
}

// parseTapeFile reads and parses a tape, closing the file before returning.
func parseTapeFile(path string) ([]tape.Command, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	return tape.Parse(f)
}
