// Package fuzz drives a terminal program with randomised but structured input
// and watches for the ways a TUI breaks: crashing, hanging, corrupting the
// screen model, growing without bound, or exiting without restoring the
// terminal. When it finds something it minimises the input that caused it and
// writes a tape file that reproduces it, because a fuzzer that reports a
// failure without a reproduction is not actionable.
//
// Everything the fuzzer sends is expressible as a tape command, and it replays
// candidates through the same tape player that runs a tape file by hand. That
// is what makes a generated repro trustworthy: it is not a description of what
// the fuzzer did, it is the same execution path.
package fuzz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// Options configures a fuzzing session.
type Options struct {
	// Argv is the program under test.
	Argv []string
	// Seed makes a session reproducible. Two sessions with the same seed,
	// options, and program generate the same input.
	Seed uint64
	// Iterations bounds how many programs are spawned and driven. Zero means
	// unlimited, in which case Duration must bound the run.
	Iterations int
	// Duration bounds wall-clock time. Zero means unlimited, in which case
	// Iterations must bound the run.
	Duration time.Duration
	// Gen configures input generation.
	Gen Config
	// Limits bounds what the detectors accept.
	Limits Limits
	// StopOnFirst ends the session as soon as one failure is found, instead of
	// continuing to look for distinct ones.
	StopOnFirst bool
	// Shrink enables minimisation of a failing input. On by default via
	// DefaultOptions; disabling it makes a session faster but its output much
	// less useful.
	Shrink bool
	// ShrinkBudget bounds how many candidate replays minimisation may spend on
	// one failure.
	ShrinkBudget int
	// Corpus is a directory where reproductions are written and from which
	// known cases are replayed as regressions first. Empty disables both.
	Corpus string
	// Out receives progress lines. Nil discards them.
	Out io.Writer
	// SettleTimeout bounds each individual wait inside an iteration.
	SettleTimeout time.Duration
}

// DefaultOptions returns options that are safe to run against an unknown
// program: bounded, shrinking enabled, memory checking off.
func DefaultOptions(argv []string) Options {
	return Options{
		Argv:          argv,
		Seed:          uint64(time.Now().UnixNano()),
		Iterations:    50,
		Gen:           Config{Cols: 80, Rows: 24, ActionsPerRun: 60},
		Limits:        DefaultLimits(),
		Shrink:        true,
		ShrinkBudget:  200,
		SettleTimeout: 2 * time.Second,
	}
}

func (o Options) withDefaults() Options {
	if o.SettleTimeout <= 0 {
		o.SettleTimeout = 2 * time.Second
	}
	if o.ShrinkBudget <= 0 {
		o.ShrinkBudget = 200
	}
	if o.Limits.HangAfter <= 0 {
		o.Limits.HangAfter = DefaultLimits().HangAfter
	}
	o.Gen = o.Gen.withDefaults()
	return o
}

// Result summarises a session.
type Result struct {
	// Iterations is how many programs were actually spawned and driven.
	Iterations int
	// Failures holds one entry per distinct failure kind found.
	Failures []*Failure
	// Elapsed is the wall-clock duration of the session.
	Elapsed time.Duration
	// Seed echoes the seed used, so a session can be replayed.
	Seed uint64
}

// Run fuzzes the program until the budget is exhausted or ctx is cancelled.
func Run(ctx context.Context, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	if len(opts.Argv) == 0 {
		return nil, errors.New("fuzz: no program given")
	}
	if opts.Iterations <= 0 && opts.Duration <= 0 {
		return nil, errors.New("fuzz: set Iterations or Duration to bound the run")
	}

	start := time.Now()
	res := &Result{Seed: opts.Seed}
	seen := map[FailureKind]bool{}

	// Replay the corpus first. Cases found on an earlier run are the most
	// likely to fail again, so checking them before generating anything new
	// turns the corpus into a regression suite.
	if opts.Corpus != "" {
		found, err := replayCorpus(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			if seen[f.Kind] {
				continue
			}
			seen[f.Kind] = true
			res.Failures = append(res.Failures, f)
			logf(opts.Out, "corpus case still fails: %s: %s\n", f.Kind, f.Detail)
		}
		if len(res.Failures) > 0 && opts.StopOnFirst {
			res.Elapsed = time.Since(start)
			return res, nil
		}
	}

	deadline := time.Time{}
	if opts.Duration > 0 {
		deadline = start.Add(opts.Duration)
	}

	for i := 0; ; i++ {
		if opts.Iterations > 0 && i >= opts.Iterations {
			break
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			break
		}
		if err := ctx.Err(); err != nil {
			break
		}

		res.Iterations++
		// Derive a per-iteration seed from the session seed so each iteration
		// is independently reproducible.
		iterSeed := opts.Seed + uint64(i)*0x9e3779b97f4a7c15
		cmds := newGenerator(opts.Gen, iterSeed).Run(opts.Argv)

		failure, spawnErr := driveReportingSpawn(ctx, opts, cmds)
		if spawnErr != nil {
			return nil, fmt.Errorf("fuzz: cannot start %s: %w", strings.Join(opts.Argv, " "), spawnErr)
		}
		if failure == nil {
			continue
		}
		failure.Seed = iterSeed
		failure.Iteration = i
		failure.Original = len(cmds)

		if seen[failure.Kind] {
			// Already have a reproduction for this kind; a second one of the
			// same kind is usually the same bug, and reporting it again adds
			// noise rather than information.
			continue
		}
		seen[failure.Kind] = true

		logf(opts.Out, "iteration %d: %s: %s\n", i, failure.Kind, failure.Detail)

		if opts.Shrink {
			before := len(failure.Commands)
			failure = shrink(ctx, opts, failure)
			logf(opts.Out, "  minimised %d commands to %d\n", before, len(failure.Commands))
		}

		// Confirm the minimised input still reproduces, so the tape we hand the
		// user is one we have actually seen fail in its final form. The
		// confirmation run's screen and detail replace the originals, because
		// the ones captured before minimisation describe input the tape no
		// longer contains and would contradict it.
		if confirmed := drive(ctx, opts, failure.Commands); confirmed != nil && confirmed.Kind == failure.Kind {
			failure.Verified = true
			failure.Screen = confirmed.Screen
			failure.Detail = confirmed.Detail
		} else {
			logf(opts.Out, "  warning: minimised input did not reproduce on confirmation; the failure may be timing dependent\n")
		}

		res.Failures = append(res.Failures, failure)

		if opts.Corpus != "" {
			path, err := writeCorpus(opts.Corpus, failure)
			if err != nil {
				logf(opts.Out, "  could not write corpus entry: %v\n", err)
			} else {
				logf(opts.Out, "  wrote %s\n", path)
			}
		}

		if opts.StopOnFirst {
			break
		}
	}

	res.Elapsed = time.Since(start)
	return res, nil
}

// drive plays one generated command sequence against a fresh program and
// returns the first failure observed, or nil. It always tears the program down
// before returning, including on panic, so a session cannot leak processes.
func drive(ctx context.Context, opts Options, cmds []tape.Command) (failure *Failure) {
	f, _ := driveReportingSpawn(ctx, opts, cmds)
	return f
}

// driveReportingSpawn is drive with the spawn error surfaced separately from
// findings, so a session can tell "the program is fine" apart from "the program
// never started".
func driveReportingSpawn(ctx context.Context, opts Options, cmds []tape.Command) (failure *Failure, spawnErr error) {
	p := tape.NewPlayer()
	p.Out = io.Discard
	// A generated run has no golden files and must never touch the filesystem.
	p.GoldenDir = ""
	defer func() { _ = p.Close() }()

	var mon *monitor
	executed := make([]tape.Command, 0, len(cmds))

	for _, c := range cmds {
		if err := ctx.Err(); err != nil {
			return nil, nil
		}
		executed = append(executed, c)

		// Waits inside a fuzz run are bounded by the session's settle timeout
		// rather than the player's default, so one unresponsive program cannot
		// stall the whole session.
		if isWait(c) && !c.HasTimeout {
			c.Timeout = opts.SettleTimeout
			c.HasTimeout = true
		}

		err := p.Exec(c)

		if mon == nil && p.Terminal() != nil {
			mon = newMonitor(p.Terminal(), opts.Limits, opts.Gen.Cols, opts.Gen.Rows)
		}
		if mon == nil {
			if err != nil {
				// Spawn itself failed. That is a problem with the command line
				// rather than a finding about the program, and it is recorded
				// so the session can report it instead of quietly running
				// thousands of iterations against nothing and concluding the
				// program is fine.
				return nil, err
			}
			continue
		}
		mon.noteCommand(c)

		// A wait timing out is not itself a failure: the program may simply
		// have had nothing to say. The hang detector decides that, using the
		// output counter rather than any single wait.
		if err != nil && !isTimeout(err) {
			// Writing to a program that has exited fails; let the exit check
			// classify it rather than reporting the write error.
			if _, exited := p.Terminal().ExitStatus(); !exited {
				return withCommands(&Failure{
					Kind:   FailCrash,
					Detail: fmt.Sprintf("driving the program failed: %v", err),
					Screen: p.Terminal().Screen().Text(),
				}, executed), nil
			}
		}

		if f := mon.check(); f != nil {
			return withCommands(f, executed), nil
		}

		if _, exited := p.Terminal().ExitStatus(); exited {
			break
		}
	}

	if mon == nil {
		return nil, nil
	}

	// Settle. Input is sent far faster than a program consumes it, so at this
	// point the last commands are still in the PTY buffer and the program has
	// had no chance to crash, hang, or exit in response to them. Without this
	// wait the iteration would tear the program down before its reaction, and
	// the detectors would grade a program that had not yet run.
	settle(p.Terminal(), opts.SettleTimeout)

	if f := mon.check(); f != nil {
		return withCommands(f, executed), nil
	}
	// Hang detection runs last and only here, because unlike the other checks
	// it has to wait to reach a verdict.
	if f := mon.checkHang(); f != nil {
		return withCommands(f, executed), nil
	}

	// Judge the terminal state only for a program that actually exited.
	// A program still running was never asked to quit, so it cannot be held to
	// having restored anything.
	if _, exited := p.Terminal().ExitStatus(); !exited {
		return nil, nil
	}
	if f := mon.checkExitState(); f != nil {
		return withCommands(f, executed), nil
	}
	return nil, nil
}

// settle waits for the program to finish reacting to the input it was sent:
// first for its output to go quiet, then, if it is on its way out, for it to
// actually exit. Both waits are bounded, and neither failing is an error here.
// The detectors, not this function, decide whether the outcome is a bug.
func settle(t *tuitest.Terminal, timeout time.Duration) {
	_ = t.WaitStable(timeout)
	if _, exited := t.ExitStatus(); exited {
		return
	}
	// A program that is mid-exit has stopped writing but not yet been reaped.
	// Give it a short grace period so the difference between "quit cleanly" and
	// "still running" is decided by the program rather than by our timing.
	_, _ = t.Wait(exitGrace)
}

// exitGrace is how long a quiesced program is given to finish exiting before
// the iteration concludes it is still running.
const exitGrace = 250 * time.Millisecond

// withCommands attaches the input that produced a failure, copied so later
// mutation of the caller's slice cannot corrupt a stored reproduction.
func withCommands(f *Failure, cmds []tape.Command) *Failure {
	f.Commands = append([]tape.Command(nil), cmds...)
	return f
}

func isWait(c tape.Command) bool {
	switch c.Kind {
	case tape.KindWait, tape.KindWaitStable, tape.KindWaitOutput, tape.KindWaitPrompt, tape.KindWaitCommand:
		return true
	default:
		return false
	}
}

// isTimeout reports whether an error is a wait timing out, or the child having
// exited mid-wait. During fuzzing both are expected outcomes of poking a
// program with input it did not ask for, not findings in themselves.
func isTimeout(err error) bool {
	var to *tuitest.TimeoutError
	var cl *tuitest.ClosedError
	return errors.As(err, &to) || errors.As(err, &cl)
}

func logf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}
