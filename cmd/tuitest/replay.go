package main

import (
	"errors"
	"os"

	"github.com/Gaurav-Gosain/tuitest/tape"
	xterm "github.com/charmbracelet/x/term"
)

func replayCmd(args []string) int {
	fs := newFlagSet("replay", "usage: tuitest replay [flags] script.tape")
	speed := fs.Float64("speed", 1, "divide every Sleep by this (2 is twice as fast)")
	step := fs.Bool("step", false, "pause before each command until you press enter")
	echo := fs.Bool("echo", true, "print each command as it runs")
	width := fs.Int("width", 40, "column width for side-by-side failure output")
	goldenDir := fs.String("golden-dir", "", "directory for golden files (default: ./testdata)")
	update := fs.Bool("update", false, "rewrite golden snapshots instead of comparing")
	strict := fs.Bool("strict", false, "treat Sleep as an error")
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

	r := &tape.Replayer{
		Render:    os.Stdout,
		Log:       os.Stderr,
		Speed:     *speed,
		Echo:      *echo,
		Step:      *step,
		StepIn:    os.Stdin,
		Width:     *width,
		GoldenDir: *goldenDir,
		Update:    *update,
		Strict:    *strict,
	}

	// Step mode reads whole lines from the operator, so the terminal must stay
	// in its normal cooked mode. The replayed program writes to the same
	// screen but is driven by the tape, not by this terminal, so no raw mode is
	// needed at all here.
	if *step && !xterm.IsTerminal(os.Stdin.Fd()) {
		return errf("-step needs a terminal on stdin")
	}

	if err := r.Run(cmds); err != nil {
		if errors.Is(err, tape.ErrStepAborted) {
			return 130
		}
		return 1
	}
	return 0
}
