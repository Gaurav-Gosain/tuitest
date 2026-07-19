package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/spf13/cobra"
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

func snapCommand(env *Env) *cobra.Command {
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

	c := &cobra.Command{
		Use:   "snap -- program [args...]",
		Short: "Spawn a command, wait for it to settle, print the screen",
		Long: `Run a program in a pseudo-terminal, wait until it stops drawing, then print
the screen exactly as a user would see it and exit.

Reach for snap first, before writing anything. It writes nothing and asserts
nothing, so it is the quickest way to find out what a TUI actually puts on
screen, and therefore what text a tape could reasonably wait for. It is also the
fastest way to check a claim about a layout: run it at two sizes and compare.

A program that never goes quiet, which is every TUI that animates, still gets
its screen reported. The settle wait times out and the exit code says so, but
the capture is the screen as it was, because that is the thing you asked for.

A program that drew nothing at all exits 5 with a note on stderr, since a blank
capture is almost never the answer the caller wanted.

Put -- before the program when the program takes flags of its own, so they are
not parsed as tuitest's.`,
		Example: `  # look at what a program draws
  tuitest snap -- htop

  # check a layout at a second size
  tuitest snap --size 120x40 -- vim

  # settle on a prompt rather than on quiet
  tuitest snap --wait '\$ ' -- bash -i

  # send input first, then capture the reaction
  tuitest snap --type 'hello\r' -- ./myapp

  # keep the SGR styling instead of flattening to text
  tuitest snap --styled -- ./myapp

  # pull just the screen out of the JSON report
  tuitest snap --json -- ./myapp | jq -r .screen`,
		RunE: func(cmd *cobra.Command, argv []string) error {
			if len(argv) == 0 {
				return usageErrorf(env, cmd, "snap needs a program to run")
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
					env.errorf("--wait %q is not a valid regex: %v", waitRe, err)
					return silent(ExitUsage)
				}
			}

			start := time.Now()
			res, err := capture(captureOpts{
				argv: argv, cols: cols, rows: rows, term: term, envs: envs,
				dir: dir, timeout: timeout, settle: settle, wait: re,
				typeIn: unescape(typeIn), styled: styled,
			})
			code := classify(err)
			// A capture that succeeded and is empty is reported as such. The
			// documented case where a program that never settles still gets
			// its screen printed is untouched: that path already carries an
			// error, and the error keeps its own more specific exit code.
			blank := err == nil && strings.TrimSpace(res.screen) == ""
			if blank {
				code = ExitBlank
			}

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
				return silent(code)
			}
			if err != nil {
				// The screen at the moment of failure is the useful part, and
				// the harness error already carries it, so report the error
				// alone rather than printing the screen twice.
				return fail(err)
			}
			if blank {
				env.errorf("the program drew nothing: the screen is empty after %s", time.Since(start).Round(time.Millisecond))
				if !res.exited {
					env.errorf("it was still running, so it may need longer than --timeout %s, or a --wait for the text it settles on", timeout)
				}
				return silent(code)
			}
			fmt.Fprintln(env.Stdout, res.screen)
			return nil
		},
	}

	f := c.Flags()
	f.Var(&size, "size", "terminal size as COLSxROWS (default \"80x24\")")
	f.StringVar(&term, "term", "xterm-256color", "value for TERM in the program's environment")
	f.Var(&envs, "env", "environment variable KEY=VALUE (repeatable)")
	f.DurationVar(&timeout, "timeout", 5*time.Second, "how long to wait for the screen to settle")
	f.DurationVar(&settle, "settle", 0, "quiet window that counts as settled (default 150ms)")
	f.StringVar(&waitRe, "wait", "", "wait until this regex matches the screen instead of waiting for quiet")
	f.StringVar(&typeIn, "type", "", "type this text before capturing (\\r, \\n, \\t and \\e are unescaped)")
	f.BoolVar(&styled, "styled", false, "render SGR styling instead of plain text")
	f.BoolVar(&jsonOut, "json", false, "print one JSON result object to stdout")
	f.StringVar(&dir, "dir", "", "working directory for the program")
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

	// Wait for something to actually appear on screen before any quiet-window
	// wait. WaitStable measures quiet from the spawn, so a program that has not
	// painted yet is reported settled and snap prints an empty capture. Waiting
	// for the first byte is not enough on its own: a full-screen program's first
	// bytes switch to the alternate screen and clear it, which is output that
	// draws nothing, and the pause before the first frame is easily longer than
	// the settle window. htop and btop both snapped blank for exactly that
	// reason.
	//
	// A program whose screen is legitimately empty is not an error, so this
	// timing out is ignored; the settle wait below still runs and the capture is
	// then blank because the program really did draw nothing.
	_ = tt.WaitFor(func(s tuitest.Screen) bool {
		return strings.TrimSpace(s.Text()) != ""
	}, o.timeout)

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

	// Capture the screen whether or not the wait succeeded. A program that
	// never goes quiet, which is every TUI that animates, times out the settle
	// wait while having drawn a perfectly good screen; returning nothing there
	// made -json report an empty screen for exactly the programs a user is most
	// likely to be pointing snap at.
	if o.styled {
		res.screen = tt.SnapshotStyled()
	} else {
		res.screen = tt.Snapshot()
	}
	res.exitCode, res.exited = tt.ExitCode()
	return res, err
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
