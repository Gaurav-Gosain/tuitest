package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Gaurav-Gosain/tuitest/fuzz"
	"github.com/spf13/cobra"
)

func fuzzCommand(env *Env) *cobra.Command {
	var (
		seed         uint64
		iterations   int
		duration     time.Duration
		cols         int
		rows         int
		actions      int
		corpus       string
		exclude      string
		noHostile    bool
		noMouse      bool
		noResize     bool
		noShrink     bool
		shrinkBudget int
		hangAfter    time.Duration
		settle       time.Duration
		memGrowth    float64
		allowDirty   bool
		stopOnFirst  bool
		quiet        bool
	)

	c := &cobra.Command{
		Use:   "fuzz -- program [args...]",
		Short: "Drive a program with randomised input and report what breaks",
		Long: `Drive a program with randomised but structured input: keys, mouse events,
resizes, and optionally malformed escape sequences.

Reach for fuzz when the tapes all pass and you want to know what nobody thought
to try. It reports crashes, hangs, screen-model corruption, and terminals left
in a bad state, and minimises each finding into a tape that replays it, so a
report arrives as a test rather than as a seed number.

Set --corpus to keep those reproductions. Entries in that directory are replayed
first on the next run, so a corpus doubles as a regression suite and a fix can
be checked by rerunning with the same directory.

The one heuristic is hang detection: there is no universal liveness probe for a
TUI, so a hang is reported only after several inputs go unanswered for the whole
--hang-after window. It is tuned to stay quiet, and it will miss hangs that
begin while a draw is already in flight.

Put -- before the program when the program takes flags of its own, so they are
not parsed as tuitest's.`,
		Example: `  # a quick pass over a program
  tuitest fuzz -- ./myapp

  # spend a fixed budget and keep the reproductions
  tuitest fuzz --duration 30s --corpus ./corpus -- ./myapp

  # reproduce a previous session exactly
  tuitest fuzz --seed 12345 -- ./myapp

  # never send the keys that quit the program
  tuitest fuzz --exclude Ctrl+c,q -- ./myapp

  # well-formed input only, no malformed escape sequences
  tuitest fuzz --no-hostile -- ./myapp`,
		RunE: func(cmd *cobra.Command, argv []string) error {
			if len(argv) == 0 {
				return usageErrorf(env, cmd, "fuzz needs a program to run")
			}

			opts := fuzz.Options{
				Argv:       argv,
				Seed:       seed,
				Iterations: iterations,
				Duration:   duration,
				Gen: fuzz.Config{
					Cols:          cols,
					Rows:          rows,
					ActionsPerRun: actions,
					ExcludeKeys:   splitList(exclude),
					NoHostile:     noHostile,
					NoMouse:       noMouse,
					NoResize:      noResize,
				},
				Limits: fuzz.Limits{
					HangAfter:      hangAfter,
					MaxRSSGrowth:   memGrowth,
					MinRSSBytes:    fuzz.DefaultLimits().MinRSSBytes,
					AllowDirtyExit: allowDirty,
				},
				StopOnFirst:   stopOnFirst,
				Shrink:        !noShrink,
				ShrinkBudget:  shrinkBudget,
				Corpus:        corpus,
				SettleTimeout: settle,
				Out:           env.Stderr,
			}
			if quiet {
				opts.Out = nil
			}
			if opts.Seed == 0 {
				opts.Seed = uint64(time.Now().UnixNano())
			}

			// Ctrl+C ends the session cleanly, so an interrupted run still
			// reports and still writes the corpus entries it already found.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if !quiet {
				fmt.Fprintf(env.Stderr, "fuzzing %s with seed %d\n", strings.Join(argv, " "), opts.Seed)
			}

			res, err := fuzz.Run(ctx, opts)
			if err != nil {
				return fail(err)
			}

			reportFuzz(env, res, corpus)
			if len(res.Failures) > 0 {
				// The findings have been printed in full, each with the tape
				// that replays it; only the exit code is left to carry.
				return silent(ExitAssert)
			}
			return nil
		},
	}

	f := c.Flags()
	f.Uint64Var(&seed, "seed", 0, "PRNG seed for a reproducible session (default: time-based)")
	f.IntVar(&iterations, "iterations", 50, "how many programs to spawn and drive (0 for unlimited)")
	f.DurationVar(&duration, "duration", 0, "wall-clock budget, for example 30s (0 for unlimited)")
	f.IntVar(&cols, "cols", 80, "initial terminal width")
	f.IntVar(&rows, "rows", 24, "initial terminal height")
	f.IntVar(&actions, "actions", 60, "maximum input actions per iteration")
	f.StringVar(&corpus, "corpus", "", "directory for reproductions, replayed as regressions on the next run")
	f.StringVar(&exclude, "exclude", "", "comma-separated key tokens never to send, for example Ctrl+c,q")
	f.BoolVar(&noHostile, "no-hostile", false, "do not send malformed or oversized escape sequences")
	f.BoolVar(&noMouse, "no-mouse", false, "do not send mouse events")
	f.BoolVar(&noResize, "no-resize", false, "do not resize the terminal")
	f.BoolVar(&noShrink, "no-shrink", false, "do not minimise a failing input (faster, much less useful output)")
	f.IntVar(&shrinkBudget, "shrink-budget", 200, "maximum replays to spend minimising one failure")
	f.DurationVar(&hangAfter, "hang-after", 5*time.Second, "silence from a live program that counts as a hang")
	f.DurationVar(&settle, "settle", 2*time.Second, "bound on each wait inside an iteration")
	f.Float64Var(&memGrowth, "max-memory-growth", 0, "fail if resident memory grows by this factor (0 disables; Linux only)")
	f.BoolVar(&allowDirty, "allow-dirty-exit", false, "do not report a program that exits without restoring the terminal")
	f.BoolVar(&stopOnFirst, "stop-on-first", false, "stop at the first failure instead of looking for distinct ones")
	f.BoolVarP(&quiet, "quiet", "q", false, "only print the summary")
	return c
}

func reportFuzz(env *Env, res *fuzz.Result, corpus string) {
	fmt.Fprintf(env.Stdout, "ran %d iterations in %s (seed %d)\n",
		res.Iterations, res.Elapsed.Round(time.Millisecond), res.Seed)

	if len(res.Failures) == 0 {
		fmt.Fprintln(env.Stdout, "no failures found")
		return
	}

	fmt.Fprintf(env.Stdout, "\n%d failure(s):\n", len(res.Failures))
	for _, f := range res.Failures {
		fmt.Fprintf(env.Stdout, "\n  %s\n    %s\n", f.Kind, f.Detail)
		fmt.Fprintf(env.Stdout, "    reproduce with seed %d, iteration %d\n", f.Seed, f.Iteration)
		if f.Original > 0 {
			fmt.Fprintf(env.Stdout, "    minimised %d commands to %d\n", f.Original, len(f.Commands))
		}
		if !f.Verified {
			fmt.Fprintf(env.Stdout, "    warning: did not reproduce on confirmation, may be timing dependent\n")
		}
		if corpus == "" {
			// Without a corpus directory there is nowhere to write the tape, so
			// print it: a failure without a reproduction is not actionable.
			fmt.Fprintf(env.Stdout, "    no --corpus set, so the reproduction is printed here:\n\n")
			for _, line := range strings.Split(strings.TrimRight(fuzz.TapeFor(f), "\n"), "\n") {
				fmt.Fprintf(env.Stdout, "      %s\n", line)
			}
		}
	}
	if corpus != "" {
		fmt.Fprintf(env.Stdout, "\nreproductions written to %s; rerun with the same --corpus to check a fix\n", corpus)
	}
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
