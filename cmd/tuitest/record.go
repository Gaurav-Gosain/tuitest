package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gaurav-Gosain/tuitest/tape"
	xterm "github.com/charmbracelet/x/term"
)

// stopKey ends a recording. Ctrl+] is the telnet escape and is almost never
// bound by a TUI, so passing everything else straight through is safe.
const stopKey = 0x1d

func recordCmd(args []string) int {
	fs := newFlagSet("record", "usage: tuitest record [flags] program [args...]")
	out := fs.String("o", "", "write the tape here (default: stdout)")
	snapshots := fs.Bool("snapshots", false, "capture a Snapshot at each settle point and write its golden")
	goldenDir := fs.String("golden-dir", "testdata", "directory for goldens written by -snapshots")
	cols := fs.Int("cols", 0, "terminal width to record at (default: this terminal)")
	rows := fs.Int("rows", 0, "terminal height to record at (default: this terminal)")
	term := fs.String("term", "xterm-256color", "TERM value for the recorded program")
	quiet := fs.Duration("quiet", tape.DefaultQuiet, "how long the screen must hold still to count as settled")
	idleSleep := fs.Duration("idle-sleep", 0, "emit Sleep for silent pauses at least this long (0 disables)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	argv := fs.Args()

	// -o is resolved before raw mode so a bad path fails before the screen is
	// taken over.
	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			return errf("%v", err)
		}
	}

	stdin := os.Stdin.Fd()
	if !xterm.IsTerminal(stdin) {
		return errf("record needs a terminal on stdin; use tuitest run for scripted playback")
	}

	c, r := *cols, *rows
	if c <= 0 || r <= 0 {
		w, h, err := xterm.GetSize(stdin)
		if err != nil {
			return errf("terminal size: %v", err)
		}
		if c <= 0 {
			c = w
		}
		if r <= 0 {
			r = h
		}
	}

	rec := tape.NewRecorder()
	rec.CaptureSnapshots = *snapshots
	rec.IdleSleep = *idleSleep

	resizes, stopResize := watchResize(stdin)
	defer stopResize()

	fmt.Fprintf(os.Stderr, "tuitest: recording %s at %dx%d; press Ctrl+] to stop\r\n",
		strings.Join(argv, " "), c, r)

	// Raw mode so the program under test sees every keystroke exactly as a
	// terminal would deliver it. It is restored before anything is reported.
	state, err := xterm.MakeRaw(stdin)
	if err != nil {
		return errf("raw mode: %v", err)
	}

	sess := &tape.Session{
		Argv:     argv,
		In:       os.Stdin,
		Out:      os.Stdout,
		Resizes:  resizes,
		Cols:     c,
		Rows:     r,
		Term:     *term,
		Quiet:    *quiet,
		StopKey:  stopKey,
		Recorder: rec,
	}
	cmds, runErr := sess.Run()

	if rerr := xterm.Restore(stdin, state); rerr != nil && runErr == nil {
		runErr = rerr
	}
	fmt.Fprint(os.Stderr, "\r\n")
	if runErr != nil {
		return errf("record: %v", runErr)
	}

	if err := writeTape(*out, argv, cmds); err != nil {
		return errf("%v", err)
	}
	if *snapshots {
		if err := writeGoldens(*goldenDir, rec.SnapshotFiles()); err != nil {
			return errf("%v", err)
		}
	}
	if n := rec.Dropped(); n > 0 {
		fmt.Fprintf(os.Stderr,
			"tuitest: warning: dropped %d input sequence(s) with no tape equivalent (mouse or paste); the tape is not a complete replay\n", n)
	}
	return 0
}

// writeTape renders the commands with a short header explaining where the tape
// came from. The header is comments only, so the file parses unchanged.
func writeTape(path string, argv []string, cmds []tape.Command) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Recorded by tuitest record: %s\n", strings.Join(argv, " "))
	b.WriteString("# Waits were inferred from where the screen settled; edit freely.\n\n")
	b.WriteString(tape.Encode(cmds))

	if path == "" {
		_, err := os.Stdout.WriteString(b.String())
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil { //nolint:gosec
		return err
	}
	fmt.Fprintf(os.Stderr, "tuitest: wrote %s (%d commands)\n", path, len(cmds))
	return nil
}

// writeGoldens saves the screens captured at each settle point, so a recording
// made with -snapshots replays green without a separate -update pass.
func writeGoldens(dir string, files map[string]string) error {
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
	fmt.Fprintf(os.Stderr, "tuitest: wrote %d golden(s) to %s\n", len(files), dir)
	return nil
}
