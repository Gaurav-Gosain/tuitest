package cli

import (
	"errors"
	"flag"
	"os"

	"github.com/Gaurav-Gosain/tuitest/tape"
	xterm "github.com/charmbracelet/x/term"
)

// ExitAborted is the conventional shell status for "interrupted by the
// operator", used when -step is abandoned partway through.
const ExitAborted = 130

func replayCommand() *Command {
	c := &Command{
		Name:    "replay",
		Summary: "play a tape onto this terminal so you can watch it",
		Usage:   "replay [flags] script.tape",
		Long: `Replay plays a tape and renders the program's screen as it goes, so you can
watch what a tape does instead of reading its assertions. It wraps the same
player that run uses, so what you see is what a headless run does.

When an assertion fails, replay shows the expected and actual screens side by
side with a '|' against every row that differs, rather than a line diff.

examples:
  tuitest replay login.tape
  tuitest replay -speed 2 login.tape       run twice as fast
  tuitest replay -step login.tape          pause before each command
  tuitest replay -update login.tape        rewrite the golden files`,
	}

	var (
		speed     float64
		step      bool
		echo      bool
		width     int
		goldenDir string
		update    bool
		strict    bool
	)
	c.flags = func() *flag.FlagSet {
		fs := newFlagSet("replay")
		fs.Float64Var(&speed, "speed", 1, "divide every Sleep by this (2 is twice as fast)")
		fs.BoolVar(&step, "step", false, "pause before each command until you press enter")
		fs.BoolVar(&echo, "echo", true, "print each command as it runs")
		fs.IntVar(&width, "width", 40, "column width for side-by-side failure output")
		fs.StringVar(&goldenDir, "golden-dir", "", "directory for golden files (default \"testdata\")")
		fs.BoolVar(&update, "update", false, "rewrite golden snapshots instead of comparing")
		fs.BoolVar(&strict, "strict", false, "treat Sleep as an error, so the tape must wait on conditions")
		return fs
	}

	c.Run = func(env *Env, args []string) int {
		fs := c.flags()
		if err := parseFlags(env, c, fs, args); err != nil {
			return ExitUsage
		}
		if fs.NArg() != 1 {
			env.errorf("replay needs exactly one tape file, got %d", fs.NArg())
			printCommandHelp(env.Stderr, c)
			return ExitUsage
		}
		path := fs.Arg(0)

		// Step mode reads whole lines from the operator, so the terminal must
		// stay in its normal cooked mode. The replayed program writes to the
		// same screen but is driven by the tape, not by this terminal, so no
		// raw mode is needed at all here.
		if step && !xterm.IsTerminal(os.Stdin.Fd()) {
			env.errorf("-step needs a terminal on stdin")
			return ExitHarness
		}

		f, err := os.Open(path) //nolint:gosec
		if err != nil {
			env.errorf("%v", err)
			return ExitHarness
		}
		cmds, err := tape.ParseNamed(f, path)
		_ = f.Close()
		if err != nil {
			env.errorf("%s", render(err))
			return classify(err)
		}

		r := &tape.Replayer{
			Render:    env.Stdout,
			Log:       env.Stderr,
			Speed:     speed,
			Echo:      echo,
			Step:      step,
			StepIn:    os.Stdin,
			Width:     width,
			GoldenDir: goldenDir,
			Update:    update,
			Strict:    strict,
		}
		if err := r.Run(cmds); err != nil {
			if errors.Is(err, tape.ErrStepAborted) {
				return ExitAborted
			}
			// The Replayer has already rendered the failing frame; printing
			// the error text again would repeat the whole screen.
			return classify(err)
		}
		return ExitOK
	}
	return c
}
