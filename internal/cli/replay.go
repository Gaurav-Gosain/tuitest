package cli

import (
	"errors"
	"os"

	"github.com/Gaurav-Gosain/tuitest/tape"
	xterm "github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// ExitAborted is the conventional shell status for "interrupted by the
// operator", used when -step is abandoned partway through.
const ExitAborted = 130

func replayCommand(env *Env) *cobra.Command {
	var (
		speed     float64
		step      bool
		echo      bool
		width     int
		goldenDir string
		update    bool
		strict    bool
	)

	c := &cobra.Command{
		Use:   "replay script.tape",
		Short: "Play a tape onto this terminal so you can watch it",
		Long: `Play a tape and render the program's screen as it goes.

Reach for replay when run has failed and the message is not enough. It wraps
the same player run uses, so what you watch is what the headless run did, and
--step pauses before each command so you can see the screen the tape was
looking at when it made its decision.

When an assertion fails, replay shows the expected and the actual screen side by
side with a '|' against every row that differs, rather than a line diff, because
a line diff of two screens is unreadable.

Replay is interactive by nature: --step needs a terminal on stdin, and the
rendering assumes a terminal it is allowed to draw on. Use run in CI.`,
		Example: `  # watch a tape run
  tuitest replay login.tape

  # run twice as fast, dividing every Sleep
  tuitest replay --speed 2 login.tape

  # pause before each command until you press enter
  tuitest replay --step login.tape

  # rewrite the golden files while watching what they became
  tuitest replay --update login.tape`,
		ValidArgsFunction: tapeFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageErrorf(env, cmd, "replay needs exactly one tape file, got %d", len(args))
			}
			path := args[0]

			// Step mode reads whole lines from the operator, so the terminal
			// must stay in its normal cooked mode. The replayed program writes
			// to the same screen but is driven by the tape, not by this
			// terminal, so no raw mode is needed at all here.
			if step && !xterm.IsTerminal(os.Stdin.Fd()) {
				return failWith(ExitHarness, errors.New("--step needs a terminal on stdin"))
			}

			f, err := os.Open(path) //nolint:gosec
			if err != nil {
				return failWith(ExitHarness, err)
			}
			cmds, err := tape.ParseNamed(f, path)
			_ = f.Close()
			if err != nil {
				return fail(err)
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
					return silent(ExitAborted)
				}
				// The Replayer has already rendered the failing frame; printing
				// the error text again would repeat the whole screen.
				return silent(classify(err))
			}
			return nil
		},
	}

	f := c.Flags()
	f.Float64Var(&speed, "speed", 1, "divide every Sleep by this (2 is twice as fast)")
	f.BoolVar(&step, "step", false, "pause before each command until you press enter")
	f.BoolVar(&echo, "echo", true, "print each command as it runs")
	f.IntVar(&width, "width", 40, "column width for side-by-side failure output")
	f.StringVar(&goldenDir, "golden-dir", "", "directory for golden files (default \"testdata\")")
	f.BoolVar(&update, "update", false, "rewrite golden snapshots instead of comparing")
	f.BoolVar(&strict, "strict", false, "treat Sleep as an error, so the tape must wait on conditions")
	return c
}
