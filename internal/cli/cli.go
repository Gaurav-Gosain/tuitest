// Package cli implements the tuitest command line. It is a real package rather
// than code in main so the whole surface, including exit codes and rendered
// error text, is testable without spawning the binary.
//
// The command set is a registry: Commands returns the built-in commands in the
// order they should appear in help, and dispatch resolves a name against it.
// Adding a command means appending one entry, which keeps help, completion, and
// the "unknown command" suggestion automatically in step with reality.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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

// Version is the reported version, overridden at build time with
// -ldflags "-X github.com/Gaurav-Gosain/tuitest/internal/cli.Version=v1.2.3".
var Version = "dev"

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

// Command is one subcommand.
type Command struct {
	// Name is the word typed after "tuitest".
	Name string
	// Summary is the one-line description shown in the command list.
	Summary string
	// Usage is the argument synopsis, without the leading "tuitest".
	Usage string
	// Long is the full help body: what it does, then examples. It is printed
	// after the usage line and the flag defaults by Help.
	Long string
	// Run executes the command with the arguments after its name.
	Run func(env *Env, args []string) int
	// flags builds the command's flag set, used by help and completion. It may
	// be nil for commands that take no flags.
	flags func() *flag.FlagSet
}

// Commands returns the built-in commands in help order.
func Commands() []*Command {
	return []*Command{
		runCommand(),
		recordCommand(),
		replayCommand(),
		snapCommand(),
		fuzzCommand(),
		doctorCommand(),
		completionCommand(),
		versionCommand(),
	}
}

// lookup finds a command by name.
func lookup(name string) *Command {
	for _, c := range Commands() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Main is the entry point. args is the argument list after the program name.
func Main(env *Env, args []string) int {
	if len(args) == 0 {
		printUsage(env.Stderr)
		return ExitUsage
	}

	name := args[0]
	switch name {
	case "help", "-h", "-help", "--help":
		return help(env, args[1:])
	case "-version", "--version":
		return lookup("version").Run(env, nil)
	}
	if strings.HasPrefix(name, "-") {
		env.errorf("unknown flag %q; tuitest takes a command first", name)
		printUsage(env.Stderr)
		return ExitUsage
	}

	cmd := lookup(name)
	if cmd == nil {
		env.errorf("unknown command %q", name)
		if s := suggest(name); s != "" {
			fmt.Fprintf(env.Stderr, "did you mean %q?\n", s)
		}
		printUsage(env.Stderr)
		return ExitUsage
	}
	return cmd.Run(env, args[1:])
}

// help implements "tuitest help [command]".
func help(env *Env, args []string) int {
	if len(args) == 0 {
		printUsage(env.Stdout)
		return ExitOK
	}
	cmd := lookup(args[0])
	if cmd == nil {
		env.errorf("unknown command %q", args[0])
		if s := suggest(args[0]); s != "" {
			fmt.Fprintf(env.Stderr, "did you mean %q?\n", s)
		}
		return ExitUsage
	}
	printCommandHelp(env.Stdout, cmd)
	return ExitOK
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `tuitest drives a terminal program through a real pseudo-terminal and asserts
on what it draws, so a TUI can be tested without writing Go.

usage:
  tuitest <command> [flags] [arguments]

commands:
`)
	cmds := Commands()
	width := 0
	for _, c := range cmds {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
	}
	fmt.Fprintf(w, "  %-*s  %s\n", width, "help", "show help for a command")
	fmt.Fprint(w, `
Run "tuitest help <command>" for a command's flags and examples.

exit codes:
  0  every assertion passed
  1  an assertion failed
  2  bad usage or a malformed tape
  3  harness error, such as no PTY or a program that would not start
  4  a wait timed out
`)
}

func printCommandHelp(w io.Writer, c *Command) {
	fmt.Fprintf(w, "usage: tuitest %s\n", c.Usage)
	if c.flags != nil {
		fs := c.flags()
		var b strings.Builder
		fs.SetOutput(&b)
		fs.PrintDefaults()
		if b.Len() > 0 {
			fmt.Fprint(w, "\nflags:\n", b.String())
		}
	}
	if c.Long != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimRight(c.Long, "\n"))
	}
}

// suggest returns the registered command name closest to name, so a typo gets
// a pointer rather than just a rejection. It only suggests when the names are
// close enough that the guess is likely right.
func suggest(name string) string {
	lower := strings.ToLower(name)
	best, bestDist := "", -1
	for _, candidate := range commandNames() {
		if d := editDistance(lower, candidate); bestDist < 0 || d < bestDist {
			best, bestDist = candidate, d
		}
	}
	// Allow roughly one edit per three characters, and always at least one, so
	// a single typo is forgiven without guessing wildly at an unrelated word.
	budget := max(len(name)/3, 1)
	if bestDist >= 0 && bestDist <= budget {
		return best
	}
	return ""
}

// editDistance is the Damerau-Levenshtein distance (optimal string alignment)
// between a and b. It counts a transposition as one edit rather than two,
// because swapped letters are the most common typo of all: "rnu" should suggest
// "run" rather than being written off as too far away.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n, m := len(ar), len(br)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, min(d[i][j-1]+1, d[i-1][j-1]+cost))
			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// newFlagSet builds a flag set that never prints on its own. Commands render
// their own help so the output is consistent across every subcommand.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseFlags parses args and, on failure, reports the error followed by the
// command's own help.
func parseFlags(env *Env, c *Command, fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		env.errorf("%v", err)
		printCommandHelp(env.Stderr, c)
		return err
	}
	return nil
}

func versionCommand() *Command {
	c := &Command{
		Name:    "version",
		Summary: "print the tuitest version",
		Usage:   "version",
		Long: `Prints the version this binary was built from.

examples:
  tuitest version
  tuitest --version`,
	}
	c.Run = func(env *Env, _ []string) int {
		fmt.Fprintf(env.Stdout, "tuitest %s\n", Version)
		return ExitOK
	}
	return c
}
