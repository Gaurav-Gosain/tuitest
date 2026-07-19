package cli

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
	"github.com/spf13/cobra"
)

// runResult is the -json shape for run.
type runResult struct {
	Command    string `json:"command"`
	Tape       string `json:"tape"`
	Status     string `json:"status"` // "pass" or "fail"
	Kind       string `json:"kind"`   // ok, assertion, usage, timeout, harness
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

func runCommand(env *Env) *cobra.Command {
	var (
		update    bool
		strict    bool
		goldenDir string
		verbose   bool
		jsonOut   bool
		timeout   time.Duration
		size      sizeFlag
		envs      envFlag
		term      string
	)

	c := &cobra.Command{
		Use:   "run script.tape",
		Short: "Play a tape script against a program",
		Long: `Play a tape script against the program the tape spawns.

This is the command CI runs. Every assertion in the tape is checked, the first
failure stops the run, and the exit code says which kind of failure it was: 1
for an assertion, 3 when the harness could not do its job, 4 for a wait that
timed out. Nothing is written to the terminal on success, so a passing suite is
silent.

A tape is line oriented, one command per line, '#' starts a comment:

  Set Size 80 24
  Spawn ./myapp
  Wait /ready/ @5s
  Type hello
  Key Enter
  Expect /you said hello/
  Snapshot after-hello
  ExpectExit 0

Commands: Set, Spawn, Type, Key, Wait, WaitStable, WaitOutput, WaitPrompt,
WaitCommand, Expect, ExpectExit, Snapshot, Resize, Mouse, Paste, Raw, Focus,
Hide, Show, Sleep.

A flag beats the tape's own Set line for the same setting, which is what makes
--size useful for checking a layout at a second size without editing the file.
--env accumulates instead, since environment entries add up.`,
		Example: `  # play a tape; exit 0 when every assertion holds
  tuitest run login.tape

  # rewrite the golden files rather than comparing against them
  tuitest run --update login.tape

  # reject Sleep, forcing the tape to wait on conditions instead
  tuitest run --strict login.tape

  # override the tape's size, to check a layout at a second one
  tuitest run --size 120x40 login.tape

  # pass an environment variable to the program under test
  tuitest run --env NO_COLOR=1 login.tape

  # emit one JSON object for CI to parse
  tuitest run --json login.tape

  # mirror PTY traffic to stderr while debugging a tape
  tuitest run -v login.tape`,
		ValidArgsFunction: tapeFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageErrorf(env, cmd, "run needs exactly one tape file, got %d", len(args))
			}
			tapePath := args[0]

			start := time.Now()
			err := playTape(env, tapePath, playOpts{
				update:    update,
				strict:    strict,
				goldenDir: goldenDir,
				verbose:   verbose,
				timeout:   timeout,
				size:      size,
				envs:      envs,
				term:      term,
			})
			code := classify(err)

			if jsonOut {
				res := runResult{
					Command:    "run",
					Tape:       tapePath,
					Status:     map[bool]string{true: "pass", false: "fail"}[code == ExitOK],
					Kind:       kindOf(code),
					ExitCode:   code,
					DurationMs: time.Since(start).Milliseconds(),
				}
				if err != nil {
					res.Error = err.Error()
				}
				writeJSON(env.Stdout, res)
				// The result object is the report; printing the error again on
				// stderr would duplicate it into a CI log.
				return silent(code)
			}
			return fail(err)
		},
	}

	f := c.Flags()
	f.BoolVar(&update, "update", false, "rewrite golden snapshots instead of comparing")
	f.BoolVar(&strict, "strict", false, "treat Sleep as an error, so the tape must wait on conditions")
	f.StringVar(&goldenDir, "golden-dir", "", "directory for golden files (default \"testdata\")")
	f.BoolVarP(&verbose, "verbose", "v", false, "mirror PTY I/O to stderr")
	f.BoolVar(&jsonOut, "json", false, "print one JSON result object to stdout")
	f.DurationVar(&timeout, "timeout", 0, "default wait timeout, overriding the tape's Set WaitTimeout")
	f.Var(&size, "size", "terminal size as COLSxROWS, overriding the tape's Set Size")
	f.Var(&envs, "env", "environment variable KEY=VALUE for the program under test (repeatable)")
	f.StringVar(&term, "term", "", "value for TERM, overriding the tape's Set Term")
	return c
}

// tapeFileCompletion offers tape files for the argument, which is the one thing
// cobra's generated completion cannot work out for itself.
func tapeFileCompletion(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return []string{"tape"}, cobra.ShellCompDirectiveFilterFileExt
}

type playOpts struct {
	update    bool
	strict    bool
	goldenDir string
	verbose   bool
	timeout   time.Duration
	size      sizeFlag
	envs      envFlag
	term      string
}

// playTape parses and plays one tape. Command-line spawn settings are handed to
// the player as overrides rather than merged into the command list, because an
// explicit flag has to beat the tape's own Set line, not the other way round.
func playTape(env *Env, path string, o playOpts) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	cmds, err := tape.ParseNamed(f, path)
	if err != nil {
		return err
	}

	p := tape.NewPlayer()
	if o.goldenDir != "" {
		p.GoldenDir = o.goldenDir
	}
	if o.update {
		p.Update = true
	}
	p.Strict = o.strict
	if o.verbose {
		p.Out = env.Stderr
	}
	p.OverrideCols, p.OverrideRows = o.size.cols, o.size.rows
	p.OverrideTerm = o.term
	p.OverrideWaitTimeout = o.timeout
	p.ExtraEnv = o.envs
	return p.Run(cmds)
}

// writeJSON emits one indented object followed by a newline, so the output is
// both readable in a terminal and valid for a pipe into jq.
func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
