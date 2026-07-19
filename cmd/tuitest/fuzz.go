package main

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

// runFuzz implements "tuitest fuzz [flags] -- program [args...]".
func runFuzz(args []string) int {
	fs := flag.NewFlagSet("tuitest fuzz", flag.ContinueOnError)
	seed := fs.Uint64("seed", 0, "PRNG seed for a reproducible session (default: time-based)")
	iterations := fs.Int("iterations", 50, "how many programs to spawn and drive (0 for unlimited)")
	duration := fs.Duration("duration", 0, "wall-clock budget, for example 30s (0 for unlimited)")
	cols := fs.Int("cols", 80, "initial terminal width")
	rows := fs.Int("rows", 24, "initial terminal height")
	actions := fs.Int("actions", 60, "maximum input actions per iteration")
	corpus := fs.String("corpus", "", "directory for reproductions, replayed as regressions on the next run")
	exclude := fs.String("exclude", "", "comma-separated key tokens never to send, for example Ctrl+c,q")
	noHostile := fs.Bool("no-hostile", false, "do not send malformed or oversized escape sequences")
	noMouse := fs.Bool("no-mouse", false, "do not send mouse events")
	noResize := fs.Bool("no-resize", false, "do not resize the terminal")
	noShrink := fs.Bool("no-shrink", false, "do not minimise a failing input (faster, much less useful output)")
	shrinkBudget := fs.Int("shrink-budget", 200, "maximum replays to spend minimising one failure")
	hangAfter := fs.Duration("hang-after", 5*time.Second, "silence from a live program that counts as a hang")
	settle := fs.Duration("settle", 2*time.Second, "bound on each wait inside an iteration")
	memGrowth := fs.Float64("max-memory-growth", 0, "fail if resident memory grows by this factor (0 disables; Linux only)")
	allowDirty := fs.Bool("allow-dirty-exit", false, "do not report a program that exits without restoring the terminal")
	stopOnFirst := fs.Bool("stop-on-first", false, "stop at the first failure instead of looking for distinct ones")
	quiet := fs.Bool("q", false, "only print the summary")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tuitest fuzz [flags] program [args...]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Drives the program with randomised but structured input and reports")
		fmt.Fprintln(os.Stderr, "crashes, hangs, screen-model corruption, and terminals left in a bad")
		fmt.Fprintln(os.Stderr, "state. Each finding is minimised into a tape file that replays it.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fs.Usage()
		return 2
	}

	opts := fuzz.Options{
		Argv:       argv,
		Seed:       *seed,
		Iterations: *iterations,
		Duration:   *duration,
		Gen: fuzz.Config{
			Cols:          *cols,
			Rows:          *rows,
			ActionsPerRun: *actions,
			ExcludeKeys:   splitList(*exclude),
			NoHostile:     *noHostile,
			NoMouse:       *noMouse,
			NoResize:      *noResize,
		},
		Limits: fuzz.Limits{
			HangAfter:      *hangAfter,
			MaxRSSGrowth:   *memGrowth,
			MinRSSBytes:    fuzz.DefaultLimits().MinRSSBytes,
			AllowDirtyExit: *allowDirty,
		},
		StopOnFirst:   *stopOnFirst,
		Shrink:        !*noShrink,
		ShrinkBudget:  *shrinkBudget,
		Corpus:        *corpus,
		SettleTimeout: *settle,
		Out:           os.Stderr,
	}
	if *quiet {
		opts.Out = nil
	}
	if opts.Seed == 0 {
		opts.Seed = uint64(time.Now().UnixNano())
	}

	// Ctrl+C ends the session cleanly, so an interrupted run still reports and
	// still writes the corpus entries it already found.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !*quiet {
		fmt.Fprintf(os.Stderr, "fuzzing %s with seed %d\n", strings.Join(argv, " "), opts.Seed)
	}

	res, err := fuzz.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tuitest: %v\n", err)
		return 2
	}

	report(res, *corpus)
	if len(res.Failures) > 0 {
		return 1
	}
	return 0
}

func report(res *fuzz.Result, corpus string) {
	fmt.Printf("ran %d iterations in %s (seed %d)\n",
		res.Iterations, res.Elapsed.Round(time.Millisecond), res.Seed)

	if len(res.Failures) == 0 {
		fmt.Println("no failures found")
		return
	}

	fmt.Printf("\n%d failure(s):\n", len(res.Failures))
	for _, f := range res.Failures {
		fmt.Printf("\n  %s\n    %s\n", f.Kind, f.Detail)
		fmt.Printf("    reproduce with seed %d, iteration %d\n", f.Seed, f.Iteration)
		if f.Original > 0 {
			fmt.Printf("    minimised %d commands to %d\n", f.Original, len(f.Commands))
		}
		if !f.Verified {
			fmt.Printf("    warning: did not reproduce on confirmation, may be timing dependent\n")
		}
		if corpus == "" {
			// Without a corpus directory there is nowhere to write the tape, so
			// print it: a failure without a reproduction is not actionable.
			fmt.Printf("    no -corpus set, so the reproduction is printed here:\n\n")
			for _, line := range strings.Split(strings.TrimRight(fuzz.TapeFor(f), "\n"), "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}
	if corpus != "" {
		fmt.Printf("\nreproductions written to %s; rerun with the same -corpus to check a fix\n", corpus)
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
