package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Gaurav-Gosain/tuitest/internal/ptyproc"
)

// Check statuses, ordered by severity.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

// check is one diagnostic result.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	// Hint is what to do about a warn or fail, omitted when all is well.
	Hint string `json:"hint,omitempty"`
}

type doctorResult struct {
	Command string  `json:"command"`
	OK      bool    `json:"ok"`
	Checks  []check `json:"checks"`
	Kind    string  `json:"kind"`
}

func doctorCommand() *Command {
	c := &Command{
		Name:    "doctor",
		Summary: "report on the environment that tests will run in",
		Usage:   "doctor [flags]",
		Long: `Doctor reports the things that decide whether tuitest works here and whether
tests will be stable: whether a pseudo-terminal can be allocated at all, what
TERM and the terminal size are, what the bundled VT emulator understands, and
the conditions that most often make a suite flaky.

It spawns no program under test and writes no files, so it is safe to run
anywhere, including in a container as a preflight step.

Doctor exits 0 when nothing failed, and 3 when something did, so CI can gate
on it:

  tuitest doctor || exit 1

examples:
  tuitest doctor
  tuitest doctor -json | jq '.checks[] | select(.status != "ok")'`,
	}

	var jsonOut bool
	c.flags = func() *flag.FlagSet {
		fs := newFlagSet("doctor")
		fs.BoolVar(&jsonOut, "json", false, "print the report as JSON")
		return fs
	}

	c.Run = func(env *Env, args []string) int {
		fs := c.flags()
		if err := parseFlags(env, c, fs, args); err != nil {
			return ExitUsage
		}
		checks := diagnose(env)
		failed := false
		for _, ck := range checks {
			if ck.Status == statusFail {
				failed = true
			}
		}
		code := ExitOK
		if failed {
			code = ExitHarness
		}

		if jsonOut {
			writeJSON(env.Stdout, doctorResult{
				Command: "doctor", OK: !failed, Checks: checks, Kind: kindOf(code),
			})
			return code
		}
		width := 0
		for _, ck := range checks {
			if len(ck.Name) > width {
				width = len(ck.Name)
			}
		}
		for _, ck := range checks {
			mark := map[string]string{statusOK: "ok  ", statusWarn: "warn", statusFail: "FAIL"}[ck.Status]
			fmt.Fprintf(env.Stdout, "%s  %-*s  %s\n", mark, width, ck.Name, ck.Detail)
			if ck.Hint != "" {
				fmt.Fprintf(env.Stdout, "      %-*s  %s\n", width, "", ck.Hint)
			}
		}
		if failed {
			fmt.Fprintln(env.Stdout, "\nsomething above failed; tuitest will not work here until it is fixed")
		}
		return code
	}
	return c
}

// diagnose runs every check. It is separate from the command so tests can call
// it directly with a synthetic environment.
func diagnose(env *Env) []check {
	checks := []check{
		checkPTY(),
		checkPlatform(),
		checkTerm(env),
		checkSize(env),
		checkEmulator(),
	}
	return append(checks, checkFlakiness(env)...)
}

// checkPTY is the one that actually matters: without a PTY nothing works. It
// allocates one and closes it immediately, spawning no child.
func checkPTY() check {
	if err := ptyproc.Probe(80, 24); err != nil {
		return check{
			Name:   "pty",
			Status: statusFail,
			Detail: fmt.Sprintf("cannot allocate a pseudo-terminal: %v", err),
			Hint:   "a container usually needs /dev/pts mounted; docker run without --init or with a restricted /dev is the common cause",
		}
	}
	return check{Name: "pty", Status: statusOK, Detail: "a pseudo-terminal can be allocated"}
}

func checkPlatform() check {
	plat := runtime.GOOS + "/" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		return check{
			Name:   "platform",
			Status: statusWarn,
			Detail: plat + ", where process-group teardown is a stub",
			Hint:   "a program that spawns children may leak them on Windows; Unix is the supported platform",
		}
	}
	return check{Name: "platform", Status: statusOK, Detail: plat}
}

func checkTerm(env *Env) check {
	term := env.getenv("TERM")
	switch {
	case term == "":
		return check{
			Name:   "TERM",
			Status: statusOK,
			Detail: "unset here; the program under test gets xterm-256color unless the tape says otherwise",
		}
	case term == "dumb":
		return check{
			Name:   "TERM",
			Status: statusWarn,
			Detail: "dumb",
			Hint:   "tuitest sets its own TERM for the child, so this only affects tuitest's own output",
		}
	default:
		return check{Name: "TERM", Status: statusOK, Detail: term}
	}
}

// checkSize reports the size tuitest defaults to. The child's size is set by
// tuitest rather than inherited, which is the property that makes a test
// reproducible on a machine whose window happens to be a different shape.
func checkSize(env *Env) check {
	detail := "the program under test always gets an explicit size, 80x24 by default"
	if cols, rows := env.getenv("COLUMNS"), env.getenv("LINES"); cols != "" && rows != "" {
		detail = fmt.Sprintf("%s (this shell reports %sx%s, which is not inherited)", detail, cols, rows)
	}
	return check{Name: "size", Status: statusOK, Detail: detail}
}

// checkEmulator states what the bundled VT understands, since "does the
// emulator support what I am asking of it" is a question users otherwise have
// to answer by reading the source.
func checkEmulator() check {
	supported := []string{
		"SGR styling", "alternate screen", "scroll regions", "wide runes",
		"mouse reporting", "OSC 133 semantic markers", "scrollback",
	}
	return check{
		Name:   "emulator",
		Status: statusOK,
		Detail: "bundled VT supports " + strings.Join(supported, ", "),
	}
}

// checkFlakiness collects the conditions that turn a correct suite into an
// intermittent one.
func checkFlakiness(env *Env) []check {
	var out []check

	// A temp dir that cannot be written breaks golden updates and fixtures.
	tmp := os.TempDir()
	f, err := os.CreateTemp(tmp, "tuitest-doctor-")
	if err != nil {
		out = append(out, check{
			Name:   "tempdir",
			Status: statusFail,
			Detail: fmt.Sprintf("%s is not writable: %v", tmp, err),
			Hint:   "golden updates and test fixtures need a writable temp directory",
		})
	} else {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		out = append(out, check{Name: "tempdir", Status: statusOK, Detail: tmp + " is writable"})
	}

	// One usable CPU makes every timing-sensitive wait slower and less
	// predictable, which is the classic cause of a suite that only fails in CI.
	if n := runtime.NumCPU(); n < 2 {
		out = append(out, check{
			Name:   "cpu",
			Status: statusWarn,
			Detail: fmt.Sprintf("%d usable CPU", n),
			Hint:   "waits may need longer timeouts on a single-CPU runner",
		})
	} else {
		out = append(out, check{Name: "cpu", Status: statusOK, Detail: fmt.Sprintf("%d usable CPUs", n)})
	}

	// go is needed only to build fixtures, so its absence is a warning.
	if path, err := exec.LookPath("go"); err != nil {
		out = append(out, check{
			Name:   "go toolchain",
			Status: statusWarn,
			Detail: "not on PATH",
			Hint:   "only needed to build Go fixtures; running a tape against an existing binary does not need it",
		})
	} else {
		out = append(out, check{Name: "go toolchain", Status: statusOK, Detail: path})
	}

	if env.getenv("CI") != "" {
		out = append(out, check{
			Name:   "CI",
			Status: statusOK,
			Detail: "CI is set; prefer waits over Sleep and consider -strict to enforce it",
		})
	}
	return out
}
