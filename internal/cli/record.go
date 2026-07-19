package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
	xterm "github.com/charmbracelet/x/term"
)

// stopKey ends a recording. Ctrl+] is the telnet escape and is almost never
// bound by a TUI, so passing everything else straight through is safe.
const stopKey = 0x1d

func recordCommand() *Command {
	c := &Command{
		Name:    "record",
		Summary: "drive a program by hand and write what you did as a tape",
		Usage:   "record [flags] -- program [args...]",
		Long: `Record spawns a program in a pseudo-terminal, connects it to this terminal so
you can use it normally, and writes what you did as a tape. Press Ctrl+] to
stop.

Waits are inferred, not timed. Where the screen settled, record prefers a Wait
on text that is new and distinctive; if nothing anchorable appeared it falls
back to WaitStable, and where the screen never changed it emits nothing, since
think time is not part of a test. Sleep is only emitted with -idle-sleep.

Use -snapshots to capture the screen behind each settle point and write the
golden files at the same time, so the recording replays green immediately.

Mouse input is not represented in the tape grammar. Mouse reports are passed
through to the program but counted, and record warns that the tape is not a
complete replay.

examples:
  tuitest record -o login.tape -- ./myapp
  tuitest record -snapshots -o login.tape -- ./myapp   also write the goldens
  tuitest record -cols 80 -rows 24 -o t.tape -- vim    record at a fixed size
  tuitest record -- htop                               write the tape to stdout`,
	}

	var (
		out       string
		snapshots bool
		goldenDir string
		cols      int
		rows      int
		term      string
		quiet     time.Duration
		idleSleep time.Duration
	)
	c.flags = func() *flag.FlagSet {
		fs := newFlagSet("record")
		fs.StringVar(&out, "o", "", "write the tape here (default: stdout)")
		fs.BoolVar(&snapshots, "snapshots", false, "capture a Snapshot at each settle point and write its golden")
		fs.StringVar(&goldenDir, "golden-dir", "testdata", "directory for goldens written by -snapshots")
		fs.IntVar(&cols, "cols", 0, "terminal width to record at (default: this terminal)")
		fs.IntVar(&rows, "rows", 0, "terminal height to record at (default: this terminal)")
		fs.StringVar(&term, "term", "xterm-256color", "TERM value for the recorded program")
		fs.DurationVar(&quiet, "quiet", tape.DefaultQuiet, "how long the screen must hold still to count as settled")
		fs.DurationVar(&idleSleep, "idle-sleep", 0, "emit Sleep for silent pauses at least this long (0 disables)")
		return fs
	}

	c.Run = func(env *Env, args []string) int {
		fs := c.flags()
		if err := parseFlags(env, c, fs, args); err != nil {
			return ExitUsage
		}
		if fs.NArg() < 1 {
			env.errorf("record needs a program to run")
			printCommandHelp(env.Stderr, c)
			return ExitUsage
		}
		argv := fs.Args()

		// -o is resolved before raw mode so a bad path fails before the screen
		// is taken over.
		if out != "" {
			if dir := filepath.Dir(out); dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					env.errorf("%v", err)
					return ExitHarness
				}
			}
		}

		stdin := os.Stdin.Fd()
		if !xterm.IsTerminal(stdin) {
			env.errorf("record needs a terminal on stdin; use tuitest run to play a tape back headlessly")
			return ExitHarness
		}

		cc, rr := cols, rows
		if cc <= 0 || rr <= 0 {
			w, h, err := xterm.GetSize(stdin)
			if err != nil {
				env.errorf("terminal size: %v", err)
				return ExitHarness
			}
			if cc <= 0 {
				cc = w
			}
			if rr <= 0 {
				rr = h
			}
		}

		rec := tape.NewRecorder()
		rec.CaptureSnapshots = snapshots
		rec.IdleSleep = idleSleep

		resizes, stopResize := watchResize(stdin)
		defer stopResize()

		fmt.Fprintf(env.Stderr, "tuitest: recording %s at %dx%d; press Ctrl+] to stop\r\n",
			strings.Join(argv, " "), cc, rr)

		// Raw mode so the program under test sees every keystroke exactly as a
		// terminal would deliver it. It is restored before anything is reported.
		state, err := xterm.MakeRaw(stdin)
		if err != nil {
			env.errorf("raw mode: %v", err)
			return ExitHarness
		}

		sess := &tape.Session{
			Argv:     argv,
			In:       os.Stdin,
			Out:      os.Stdout,
			Resizes:  resizes,
			Cols:     cc,
			Rows:     rr,
			Term:     term,
			Quiet:    quiet,
			StopKey:  stopKey,
			Recorder: rec,
		}
		cmds, runErr := sess.Run()

		if rerr := xterm.Restore(stdin, state); rerr != nil && runErr == nil {
			runErr = rerr
		}
		fmt.Fprint(env.Stderr, "\r\n")
		if runErr != nil {
			env.errorf("record: %v", runErr)
			return classify(runErr)
		}

		if err := writeTapeFile(env, out, argv, cmds); err != nil {
			env.errorf("%v", err)
			return ExitHarness
		}
		if snapshots {
			if err := writeGoldens(env, goldenDir, rec.SnapshotFiles()); err != nil {
				env.errorf("%v", err)
				return ExitHarness
			}
		}
		return ExitOK
	}
	return c
}

// writeTape renders the commands with a short header explaining where the tape
// came from. The header is comments only, so the file parses unchanged.
func writeTapeFile(env *Env, path string, argv []string, cmds []tape.Command) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Recorded by tuitest record: %s\n", strings.Join(argv, " "))
	b.WriteString("# Waits were inferred from where the screen settled; edit freely.\n\n")
	b.WriteString(tape.Sprint(cmds))

	if path == "" {
		_, err := env.Stdout.Write([]byte(b.String()))
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil { //nolint:gosec
		return err
	}
	fmt.Fprintf(env.Stderr, "tuitest: wrote %s (%d commands)\n", path, len(cmds))
	return nil
}

// writeGoldens saves the screens captured at each settle point, so a recording
// made with -snapshots replays green without a separate -update pass.
func writeGoldens(env *Env, dir string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		p := filepath.Join(dir, name+".golden")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec
			return err
		}
	}
	fmt.Fprintf(env.Stderr, "tuitest: wrote %d golden(s) to %s\n", len(files), dir)
	return nil
}
