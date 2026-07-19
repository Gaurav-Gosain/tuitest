package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Gaurav-Gosain/tuitest/fuzz"
)

func fuzzCommand() *Command {
	c := &Command{
		Name:    "fuzz",
		Summary: "drive a program with randomised input and report what breaks",
		Usage:   "fuzz [flags] -- program [args...]",
		Long: `Fuzz drives a program with randomised but structured input: keys, mouse
events, resizes, and optionally malformed escape sequences. It reports crashes,
hangs, screen-model corruption, and terminals left in a bad state, and minimises
each finding into a tape that replays it.

Set -corpus to keep the reproductions. Entries in that directory are replayed
first on the next run, so a corpus doubles as a regression suite.

Put -- before the program when the program takes flags of its own, so they are
not parsed as tuitest's.

The one heuristic is hang detection: there is no universal liveness probe for a
TUI, so a hang is reported only after several inputs go unanswered for the whole
-hang-after window. It is tuned to stay quiet, and it will miss hangs that begin
while a draw is already in flight.

examples:
  tuitest fuzz -- ./myapp
  tuitest fuzz -duration 30s -corpus ./corpus -- ./myapp
  tuitest fuzz -seed 12345 -- ./myapp            reproduce a previous session
  tuitest fuzz -exclude Ctrl+c,q -- ./myapp      never send the quit keys
  tuitest fuzz -no-hostile -- ./myapp            well-formed input only`,
	}

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
	c.flags = func() *flag.FlagSet {
		fs := newFlagSet("fuzz")
		fs.Uint64Var(&seed, "seed", 0, "PRNG seed for a reproducible session (default: time-based)")
		fs.IntVar(&iterations, "iterations", 50, "how many programs to spawn and drive (0 for unlimited)")
		fs.DurationVar(&duration, "duration", 0, "wall-clock budget, for example 30s (0 for unlimited)")
		fs.IntVar(&cols, "cols", 80, "initial terminal width")
		fs.IntVar(&rows, "rows", 24, "initial terminal height")
		fs.IntVar(&actions, "actions", 60, "maximum input actions per iteration")
		fs.StringVar(&corpus, "corpus", "", "directory for reproductions, replayed as regressions on the next run")
		fs.StringVar(&exclude, "exclude", "", "comma-separated key tokens never to send, for example Ctrl+c,q")
		fs.BoolVar(&noHostile, "no-hostile", false, "do not send malformed or oversized escape sequences")
		fs.BoolVar(&noMouse, "no-mouse", false, "do not send mouse events")
		fs.BoolVar(&noResize, "no-resize", false, "do not resize the terminal")
		fs.BoolVar(&noShrink, "no-shrink", false, "do not minimise a failing input (faster, much less useful output)")
		fs.IntVar(&shrinkBudget, "shrink-budget", 200, "maximum replays to spend minimising one failure")
		fs.DurationVar(&hangAfter, "hang-after", 5*time.Second, "silence from a live program that counts as a hang")
		fs.DurationVar(&settle, "settle", 2*time.Second, "bound on each wait inside an iteration")
		fs.Float64Var(&memGrowth, "max-memory-growth", 0, "fail if resident memory grows by this factor (0 disables; Linux only)")
		fs.BoolVar(&allowDirty, "allow-dirty-exit", false, "do not report a program that exits without restoring the terminal")
		fs.BoolVar(&stopOnFirst, "stop-on-first", false, "stop at the first failure instead of looking for distinct ones")
		fs.BoolVar(&quiet, "q", false, "only print the summary")

		return fs
	}

	c.Run = func(env *Env, args []string) int {
		fs := c.flags()
		if err := parseFlags(env, c, fs, args); err != nil {
			return ExitUsage
		}
		argv := fs.Args()
		if len(argv) == 0 {
			env.errorf("fuzz needs a program to run")
			printCommandHelp(env.Stderr, c)
			return ExitUsage
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

		// Ctrl+C ends the session cleanly, so an interrupted run still reports and
		// still writes the corpus entries it already found.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if !quiet {
			fmt.Fprintf(env.Stderr, "fuzzing %s with seed %d\n", strings.Join(argv, " "), opts.Seed)
		}

		res, err := fuzz.Run(ctx, opts)
		if err != nil {
			env.errorf("%v", err)
			return classify(err)
		}

		report(env, res, corpus)
		if len(res.Failures) > 0 {
			return ExitAssert
		}
		return ExitOK
	}
	return c
}

func report(env *Env, res *fuzz.Result, corpus string) {
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
			fmt.Fprintf(env.Stdout, "    no -corpus set, so the reproduction is printed here:\n\n")
			for _, line := range strings.Split(strings.TrimRight(fuzz.TapeFor(f), "\n"), "\n") {
				fmt.Fprintf(env.Stdout, "      %s\n", line)
			}
		}
	}
	if corpus != "" {
		fmt.Fprintf(env.Stdout, "\nreproductions written to %s; rerun with the same -corpus to check a fix\n", corpus)
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
