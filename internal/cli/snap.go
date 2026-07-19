package cli

import (
	"flag"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// snapResult is the -json shape for snap.
type snapResult struct {
	Command string   `json:"command"`
	Argv    []string `json:"argv"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	Screen  string   `json:"screen"`
	Styled  bool     `json:"styled"`
	Exited  bool     `json:"exited"`
	// ExitCode is null while the program is still running, since -1 would
	// otherwise read as a real status.
	ExitCode   *int   `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Kind       string `json:"kind"`
	Error      string `json:"error,omitempty"`
}

func snapCommand() *Command {
	c := &Command{
		Name:    "snap",
		Summary: "spawn a command, wait for it to settle, print the screen",
		Usage:   "snap [flags] -- program [args...]",
		Long: `Snap runs a program in a pseudo-terminal, waits until it stops drawing, then
prints the screen exactly as a user would see it and exits. It writes nothing
and asserts nothing, so it is the quickest way to find out what a TUI actually
puts on screen, and what text a tape could wait for.

Put -- before the program when the program takes flags of its own, so they are
not parsed as tuitest's.

examples:
  tuitest snap -- htop
  tuitest snap -size 120x40 -- vim
  tuitest snap -wait '\$ ' -- bash -i        settle on a prompt instead
  tuitest snap -type 'hello\r' -- ./myapp    send input, then capture
  tuitest snap -styled -- ./myapp            include SGR styling
  tuitest snap -json -- ./myapp | jq -r .screen`,
	}

	var (
		size    sizeFlag
		term    string
		envs    envFlag
		timeout time.Duration
		settle  time.Duration
		waitRe  string
		typeIn  string
		styled  bool
		jsonOut bool
		dir     string
	)
	c.flags = func() *flag.FlagSet {
		fs := newFlagSet("snap")
		fs.Var(&size, "size", "terminal size as COLSxROWS (default \"80x24\")")
		fs.StringVar(&term, "term", "xterm-256color", "TERM value for the program")
		fs.Var(&envs, "env", "environment variable KEY=VALUE (repeatable)")
		fs.DurationVar(&timeout, "timeout", 5*time.Second, "how long to wait for the screen to settle")
		fs.DurationVar(&settle, "settle", 0, "quiet window that counts as settled (default 150ms)")
		fs.StringVar(&waitRe, "wait", "", "wait until this regex matches the screen instead of waiting for quiet")
		fs.StringVar(&typeIn, "type", "", "type this text before capturing (\\r, \\n, \\t and \\e are unescaped)")
		fs.BoolVar(&styled, "styled", false, "render SGR styling instead of plain text")
		fs.BoolVar(&jsonOut, "json", false, "print one JSON result object to stdout")
		fs.StringVar(&dir, "dir", "", "working directory for the program")
		return fs
	}

	c.Run = func(env *Env, args []string) int {
		fs := c.flags()
		if err := parseFlags(env, c, fs, args); err != nil {
			return ExitUsage
		}
		argv := fs.Args()
		if len(argv) == 0 {
			env.errorf("snap needs a program to run")
			printCommandHelp(env.Stderr, c)
			return ExitUsage
		}
		cols, rows := size.cols, size.rows
		if cols == 0 {
			cols, rows = 80, 24
		}
		var re *regexp.Regexp
		if waitRe != "" {
			var err error
			re, err = regexp.Compile(waitRe)
			if err != nil {
				env.errorf("-wait %q is not a valid regex: %v", waitRe, err)
				return ExitUsage
			}
		}

		start := time.Now()
		res, err := capture(captureOpts{
			argv: argv, cols: cols, rows: rows, term: term, envs: envs,
			dir: dir, timeout: timeout, settle: settle, wait: re,
			typeIn: unescape(typeIn), styled: styled,
		})
		code := classify(err)

		if jsonOut {
			out := snapResult{
				Command: "snap", Argv: argv, Cols: cols, Rows: rows,
				Screen: res.screen, Styled: styled,
				Exited:     res.exited,
				DurationMs: time.Since(start).Milliseconds(), Kind: kindOf(code),
			}
			if res.exited {
				code := res.exitCode
				out.ExitCode = &code
			}
			if err != nil {
				out.Error = render(err)
			}
			writeJSON(env.Stdout, out)
			return code
		}
		if err != nil {
			// The screen at the moment of failure is the useful part, and the
			// harness error already carries it, so print the error alone.
			env.errorf("%s", render(err))
			return code
		}
		fmt.Fprintln(env.Stdout, res.screen)
		return ExitOK
	}
	return c
}

type captureOpts struct {
	argv    []string
	cols    int
	rows    int
	term    string
	envs    []string
	dir     string
	timeout time.Duration
	settle  time.Duration
	wait    *regexp.Regexp
	typeIn  string
	styled  bool
}

type captureResult struct {
	screen   string
	exited   bool
	exitCode int
}

// capture spawns the program, waits for it to settle, and returns the screen.
// The terminal is always closed, including on every error path, so no child
// process outlives the command.
func capture(o captureOpts) (captureResult, error) {
	var res captureResult

	opts := []tuitest.Option{
		tuitest.WithSize(o.cols, o.rows),
		tuitest.WithTerm(o.term),
	}
	if len(o.envs) > 0 {
		opts = append(opts, tuitest.WithEnv(o.envs...))
	}
	if o.dir != "" {
		opts = append(opts, tuitest.WithDir(o.dir))
	}
	if o.settle > 0 {
		opts = append(opts, tuitest.WithStabilizeInterval(o.settle))
	}

	tt, err := tuitest.Start(o.argv, opts...)
	if err != nil {
		return res, err
	}
	defer tt.Close() //nolint:errcheck

	if o.typeIn != "" {
		// Wait for the program to draw something before typing, so input is
		// not delivered to a program that has not installed its handlers.
		if err := tt.WaitStable(o.timeout); err != nil {
			return res, err
		}
		before := tt.Screen().Text()
		if err := tt.Type(o.typeIn); err != nil {
			return res, err
		}
		if o.wait == nil {
			// The screen has already been quiet for longer than the settle
			// window, so a bare WaitStable would return instantly and capture
			// the screen as it was before the input landed. Wait for the
			// program to actually react first. A program is allowed not to
			// react, so this timing out is not an error, it just means nothing
			// changed; pair -type with -wait when the response matters.
			_ = tt.WaitFor(func(s tuitest.Screen) bool { return s.Text() != before }, o.timeout)
		}
	}

	if o.wait != nil {
		err = tt.WaitForMatch(o.wait, tuitest.ScopeScreen, o.timeout)
	} else {
		err = tt.WaitStable(o.timeout)
	}
	if err != nil {
		return res, err
	}

	if o.styled {
		res.screen = tt.SnapshotStyled()
	} else {
		res.screen = tt.Snapshot()
	}
	res.exitCode, res.exited = tt.ExitCode()
	return res, nil
}

// unescape expands the handful of backslash escapes a shell will not expand
// inside a single-quoted flag value, so -type 'hello\r' sends a real return.
func unescape(s string) string {
	r := strings.NewReplacer(
		`\r`, "\r",
		`\n`, "\n",
		`\t`, "\t",
		`\e`, "\x1b",
		`\\`, `\`,
	)
	return r.Replace(s)
}
