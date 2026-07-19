// Package cli implements the tuitest command line. It is a real package rather
// than code in main so the whole surface, including exit codes and rendered
// error text, is testable without spawning the binary.
//
// Command structure, help, flag parsing, shell completion and "did you mean"
// suggestions come from spf13/cobra, and charmbracelet/fang renders help and
// version output. Everything the hand-rolled dispatcher did better is layered
// on top rather than replaced:
//
//   - Env keeps every stream and the environment lookup injectable, so Main can
//     be called from a test with buffers instead of a terminal. cobra writes
//     through the same writers because the root command is given them.
//   - Exit codes stay a first-class result. cobra's RunE only reports "an
//     error", so commands return an exitError carrying the code that classify
//     chose, and Main unwraps it.
//   - Failure text is printed by this package's own renderer, not fang's, which
//     reflows a message into one width-bounded paragraph. A timeout message is
//     a screen dump; reflowing it destroys the thing the user needs to read.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
)

// Exit codes. These are part of the tool's contract with CI: a script needs to
// tell "the program under test is wrong" apart from "tuitest could not run it",
// and a timeout apart from both, without parsing stderr.
const (
	// ExitOK means every assertion passed.
	ExitOK = 0
	// ExitAssert means the program ran but an assertion failed.
	ExitAssert = 1
	// ExitUsage means the command line or the tape was malformed.
	ExitUsage = 2
	// ExitHarness means tuitest itself could not do its job: no PTY, the
	// program could not be spawned, a golden file could not be read.
	ExitHarness = 3
	// ExitTimeout means a wait exceeded its deadline.
	ExitTimeout = 4
)

// Build information, overridden at link time with
// -ldflags "-X github.com/Gaurav-Gosain/tuitest/internal/cli.Version=v1.2.3".
var (
	// Version is the reported version.
	Version = "dev"
	// Commit is the git revision the binary was built from.
	Commit = "none"
	// Date is the build timestamp.
	Date = "unknown"
)

// Env is everything a command touches outside its arguments. Tests supply their
// own writers and environment lookup so no command needs a real terminal.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads an environment variable. Nil means os.Getenv.
	Getenv func(string) string
}

// OSEnv returns the Env that the real binary runs with.
func OSEnv() *Env {
	return &Env{Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}
}

func (e *Env) getenv(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

func (e *Env) errorf(format string, args ...any) {
	fmt.Fprintf(e.Stderr, "tuitest: "+format+"\n", args...)
}

// exitError is a command failure that already knows which exit code it means.
//
// cobra's RunE signature collapses every failure into one error value, which
// would flatten the tool's five exit codes into "zero or one". Commands return
// this instead, so classify's judgement survives the trip back to main.
type exitError struct {
	code int
	// err is the failure to report, or nil when the command has already
	// printed everything the user needs.
	err error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// fail returns a command failure whose exit code is classified from the error,
// so a timeout stays a timeout and a spawn failure stays a harness error.
func fail(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: classify(err), err: err}
}

// failWith returns a command failure with an explicit exit code, for the cases
// that are not derived from an error value: a terminal replay cannot step on, a
// tape file that will not open.
func failWith(code int, err error) error {
	return &exitError{code: code, err: err}
}

// silent returns a failure with no message, for commands that have already
// written their own report and only need the exit code honoured.
func silent(code int) error { return &exitError{code: code} }

// usageErrorf reports a malformed invocation the way the flag-based CLI did:
// the complaint, then the command's own usage, then exit 2.
//
// cobra prints usage itself, but only for errors it raises during parsing. An
// argument count that only RunE can check is exactly as much a usage error, and
// it has to look the same.
func usageErrorf(env *Env, cmd *cobra.Command, format string, args ...any) error {
	env.errorf(format, args...)
	fmt.Fprint(env.Stderr, cmd.UsageString())
	return silent(ExitUsage)
}

// newRootCommand builds the command tree for one invocation. It is rebuilt per
// call rather than shared, so a test that runs Main twice gets fresh flag
// values both times.
func newRootCommand(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:   "tuitest",
		Short: "Test terminal programs through a real pseudo-terminal",
		Long: `tuitest drives a terminal program through a real pseudo-terminal and asserts
on what it draws, so a TUI can be tested without writing Go.

The loop is snap to look at what a program draws, record or an editor to write
a tape, run in CI, replay to debug a failure, and fuzz to go looking for the
inputs nobody thought to try.

Exit codes are the contract with CI, and every command uses them:

  0  every assertion passed
  1  an assertion failed
  2  bad usage or a malformed tape
  3  harness error, such as no PTY or a program that would not start
  4  a wait timed out`,
		Example: `  # check this machine can run a TUI at all, before anything else
  tuitest doctor

  # look at what a program actually draws, asserting nothing
  tuitest snap -- htop

  # play a tape and let the exit code decide whether CI passes
  tuitest run login.tape

  # watch the same tape run, to see where it goes wrong
  tuitest replay login.tape`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Bare "tuitest" is a usage error, not a success that happens to
			// print help. A script that runs the tool with an empty argument
			// list has a bug, and exit 0 would hide it.
			return usageErrorf(env, cmd, "no command given")
		},
	}

	root.AddCommand(
		runCommand(env),
		recordCommand(env),
		replayCommand(env),
		snapCommand(env),
		fuzzCommand(env),
		doctorCommand(env),
		versionCommand(env),
	)
	return root
}

// initCompletion installs cobra's generated completion command.
//
// It replaces the hand-rolled scripts. cobra's version covers four shells
// instead of three and resolves candidates by calling the binary back through a
// hidden command rather than baking a list into the script, so it completes
// flag names, flag values and arguments as well as command names, and it cannot
// fall out of step with the commands the way a generated-once script can.
//
// It is installed here, rather than left to Execute, for two reasons. The
// generated subcommands capture the root's output writer at construction time,
// so they have to be built after Main has pointed the root at the Env's
// streams. And the parent command needs the one fix cobra does not make: it is
// not runnable, so cobra answers "tuitest completion csh" with the help text
// and exit 0, when an unsupported shell name is plainly a usage error.
func initCompletion(env *Env, root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() != "completion" {
			continue
		}
		c.RunE = func(cmd *cobra.Command, _ []string) error {
			return usageErrorf(env, cmd, "completion needs a shell name: bash, zsh, fish or powershell")
		}
	}
}

// Main is the entry point. args is the argument list after the program name.
func Main(env *Env, args []string) int {
	root := newRootCommand(env)

	// cobra writes help, usage and its own parse errors through these, so the
	// Env indirection covers the framework's output as well as the commands'.
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	initCompletion(env, root)
	root.SetArgs(normalizeArgs(root, args))

	// Command failures are printed here rather than by fang, whose renderer
	// reflows an error into one paragraph and would destroy the screen dumps
	// that make a wait failure diagnosable. See interceptErrors.
	var cmdErr error
	interceptErrors(root, &cmdErr)

	err := fang.Execute(
		context.Background(),
		root,
		fang.WithVersion(fmt.Sprintf("%s\nCommit: %s\nBuilt: %s", Version, Commit, Date)),
		fang.WithErrorHandler(diagnosticErrorHandler),
	)

	if cmdErr != nil {
		return report(env, cmdErr)
	}
	if err != nil {
		// Everything fang can still be handed is a parse or dispatch failure:
		// an unknown command, an unknown flag, a bad flag value. fang has
		// already printed it.
		return ExitUsage
	}
	return ExitOK
}

// report prints a command failure and returns its exit code.
func report(env *Env, err error) int {
	var ee *exitError
	if !errors.As(err, &ee) {
		reportError(env, err)
		return classify(err)
	}
	if ee.err != nil {
		reportError(env, ee.err)
	}
	return ee.code
}

// interceptErrors routes every command's failure into stash instead of
// returning it to fang, whose renderer would reflow it. The error is stashed
// and the command reports success, so fang sees nothing to render; Main prints
// it and picks the exit status afterwards.
//
// A malformed flag is rejected by cobra before any RunE runs and still goes
// through fang, which is the right home for it: those messages are one line and
// reflowing them costs nothing.
func interceptErrors(cmd *cobra.Command, stash *error) {
	for _, sub := range cmd.Commands() {
		interceptErrors(sub, stash)
	}
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if err := run(c, args); err != nil {
			*stash = err
			// The error is already explained; usage would bury it.
			c.SilenceUsage = true
		}
		return nil
	}
}

// normalizeArgs accepts the single-dash long flags the tool has always taken.
//
// The published command line is "tuitest run -update login.tape", and every
// script and README example written against it says -update. pflag reads a
// single dash as a cluster of one-letter shorthands, so it would reject that
// spelling outright. Rewriting a leading "-name" to "--name" when name is a
// real flag of this command keeps every existing invocation working, while the
// double-dash spelling becomes available alongside it.
//
// Only tokens that name a registered multi-letter flag are touched, so a
// one-letter shorthand still parses as a shorthand, and anything after "--"
// belongs to the program under test and is left exactly as typed.
func normalizeArgs(root *cobra.Command, args []string) []string {
	if len(args) == 0 {
		return args
	}

	// The command set is flat, so the first token either names a command or the
	// whole line belongs to the root.
	cmd, rest := root, args
	if sub, _, err := root.Find(args[:1]); err == nil && sub != root {
		cmd, rest = sub, args[1:]
	}

	out := make([]string, 0, len(args))
	out = append(out, args[:len(args)-len(rest)]...)
	for i, arg := range rest {
		if arg == "--" {
			out = append(out, rest[i:]...)
			break
		}
		out = append(out, expandSingleDash(cmd, arg))
	}
	return out
}

// expandSingleDash rewrites "-name" and "-name=value" to their double-dash
// spelling when name is a multi-letter flag of cmd, and returns arg untouched
// otherwise.
func expandSingleDash(cmd *cobra.Command, arg string) string {
	if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
		return arg
	}
	name, _, _ := strings.Cut(arg[1:], "=")

	// The pre-cobra CLI parsed with the flag package, where "-help" and
	// "-version" were ordinary flags. Cobra owns both: help is registered during
	// Execute and version is supplied by fang, so neither is in the flag set
	// while normalization runs and both would reach pflag as shorthand clusters
	// ("-version" reads as an unknown shorthand 'e'). Name them directly rather
	// than depending on that initialization order.
	if !cmd.HasParent() && (name == "help" || name == "version") {
		return "-" + arg
	}

	if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil {
		return arg
	}
	return "-" + arg
}

func versionCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tuitest version",
		Long: `Print the version this binary was built from.

The version is stamped in at link time, so a binary installed with 'go install'
reports the module version it came from and a locally built one reports "dev".
Reach for it when a suite starts failing without a change to the tape, to pin
down what a CI image is actually running.

The root command's --version flag prints the same version along with the commit
and the build date.`,
		Example: `  # print the version
  tuitest version

  # print the version, commit and build date
  tuitest --version`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Fprintf(env.Stdout, "tuitest %s\n", Version)
			return nil
		},
	}
}
