package cli

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
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

func runCommand() *Command {
	c := &Command{
		Name:    "run",
		Summary: "play a tape script against a program",
		Usage:   "run [flags] script.tape",
		Long: `Run parses a tape script and plays it against the program the tape spawns.
Every assertion in the tape is checked; the first failure stops the run.

A tape is line oriented, one command per line, '#' starts a comment:

  Set Size 80 24
  Spawn ./myapp
  Wait /ready/ @5s
  Type hello
  Key Enter
  Expect /you said hello/
  Snapshot after-hello
  ExpectExit 0

Commands: Set, Spawn, Type, Key, Wait, WaitStable, WaitPrompt, WaitCommand,
Expect, ExpectExit, Snapshot, Hide, Show, Sleep.

examples:
  tuitest run login.tape
  tuitest run -update login.tape            rewrite the golden files
  tuitest run -strict login.tape            reject Sleep, forcing real waits
  tuitest run -size 120x40 login.tape       override the tape's size
  tuitest run -env NO_COLOR=1 login.tape    pass an environment variable
  tuitest run -json login.tape              emit one JSON object for CI
  tuitest run -v login.tape                 mirror PTY traffic to stderr`,
	}

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
	c.flags = func() *flag.FlagSet {
		fs := newFlagSet("run")
		fs.BoolVar(&update, "update", false, "rewrite golden snapshots instead of comparing")
		fs.BoolVar(&strict, "strict", false, "treat Sleep as an error, so the tape must wait on conditions")
		fs.StringVar(&goldenDir, "golden-dir", "", "directory for golden files (default \"testdata\")")
		fs.BoolVar(&verbose, "v", false, "mirror PTY I/O to stderr")
		fs.BoolVar(&jsonOut, "json", false, "print one JSON result object to stdout")
		fs.DurationVar(&timeout, "timeout", 0, "default wait timeout, overriding the tape's Set WaitTimeout")
		fs.Var(&size, "size", "terminal size as COLSxROWS, overriding the tape's Set Size")
		fs.Var(&envs, "env", "environment variable KEY=VALUE for the program under test (repeatable)")
		fs.StringVar(&term, "term", "", "TERM value, overriding the tape's Set Term")
		return fs
	}

	c.Run = func(env *Env, args []string) int {
		fs := c.flags()
		if err := parseFlags(env, c, fs, args); err != nil {
			return ExitUsage
		}
		if fs.NArg() != 1 {
			env.errorf("run needs exactly one tape file, got %d", fs.NArg())
			printCommandHelp(env.Stderr, c)
			return ExitUsage
		}
		tapePath := fs.Arg(0)

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
			return code
		}
		if err != nil {
			env.errorf("%s", render(err))
		}
		return code
	}
	return c
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
