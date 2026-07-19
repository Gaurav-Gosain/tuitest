package tape

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// Player drives a tuitest.Terminal from a parsed tape. It owns nothing the Go
// API does not already expose; each command maps to one or more API calls.
type Player struct {
	// GoldenDir is where Snapshot reads and writes golden files.
	GoldenDir string
	// Update rewrites golden files instead of comparing (UPDATE_GOLDEN).
	Update bool
	// Strict makes Sleep an error, since conditions should be waited on.
	Strict bool
	// Out receives progress and diagnostic lines.
	Out io.Writer
	// Mirror, when set, receives the spawned program's output as it arrives, so
	// a caller can render the run onto a real terminal. See tuitest replay.
	Mirror io.Writer
	// SleepScale divides every Sleep duration, so 2 replays at double speed.
	// Zero or negative means unscaled.
	SleepScale float64
	// Before runs just before each command; a non-nil error aborts the run. It
	// is how replay implements step mode and command echo.
	Before func(Command) error
	// After runs just after each command with the error it produced, if any.
	After func(Command, error)

	// accumulated spawn configuration from Set commands
	cols, rows  int
	term        string
	env         []string
	stabilize   time.Duration
	waitTimeout time.Duration

	tt     *tuitest.Terminal
	hidden bool
}

// NewPlayer returns a player with sensible defaults.
func NewPlayer() *Player {
	return &Player{
		GoldenDir:   "testdata",
		Out:         io.Discard,
		cols:        80,
		rows:        24,
		term:        "xterm-256color",
		waitTimeout: 5 * time.Second,
		Update:      os.Getenv("UPDATE_GOLDEN") != "",
	}
}

// Run plays every command in order. It closes the spawned terminal on return.
func (p *Player) Run(cmds []Command) (err error) {
	defer func() {
		if p.tt != nil {
			_ = p.tt.Close()
		}
	}()
	for _, c := range cmds {
		if p.Before != nil {
			if e := p.Before(c); e != nil {
				return e
			}
		}
		e := p.run(c)
		if p.After != nil {
			p.After(c, e)
		}
		if e != nil {
			return fmt.Errorf("tape line %d: %w", c.Line, e)
		}
	}
	return nil
}

// scaleSleep applies SleepScale, so replay can run a tape faster or slower
// without rewriting it.
func (p *Player) scaleSleep(d time.Duration) time.Duration {
	if p.SleepScale <= 0 {
		return d
	}
	return time.Duration(float64(d) / p.SleepScale)
}

func (p *Player) run(c Command) error {
	switch c.Kind {
	case KindSet:
		return p.applySet(c)
	case KindSpawn:
		return p.spawn(c)
	case KindType:
		return p.needTerm(func() error { return p.tt.Type(c.Text) })
	case KindKey:
		return p.needTerm(func() error { return p.sendKeys(c.Keys) })
	case KindWait:
		return p.needTerm(func() error {
			if !c.HasRegex {
				return fmt.Errorf("Wait needs a /regex/ (or use Wait Stable)")
			}
			return p.tt.WaitForMatch(c.Regex, c.Scope, p.timeout(c))
		})
	case KindWaitStable:
		return p.needTerm(func() error { return p.tt.WaitStable(p.timeout(c)) })
	case KindWaitPrompt:
		return p.needTerm(func() error { return p.tt.WaitForPrompt(p.timeout(c)) })
	case KindWaitCommand:
		return p.needTerm(func() error { return p.tt.WaitForCommand(p.timeout(c)) })
	case KindExpect:
		return p.needTerm(func() error { return p.expect(c) })
	case KindExpectExit:
		return p.needTerm(func() error { return p.expectExit(c) })
	case KindSnapshot:
		return p.needTerm(func() error { return p.snapshot(c) })
	case KindHide:
		p.hidden = true
		return nil
	case KindShow:
		p.hidden = false
		return nil
	case KindResize:
		return p.needTerm(func() error { return p.tt.Resize(c.Cols, c.Rows) })
	case KindSleep:
		if p.Strict {
			return fmt.Errorf("Sleep is disallowed in strict mode; wait on a condition instead")
		}
		time.Sleep(p.scaleSleep(c.Dur))
		return nil
	default:
		return fmt.Errorf("unhandled command kind %d", c.Kind)
	}
}

func (p *Player) needTerm(fn func() error) error {
	if p.tt == nil {
		return fmt.Errorf("no program spawned yet (missing Spawn)")
	}
	return fn()
}

func (p *Player) applySet(c Command) error {
	if p.tt != nil {
		// Some settings only take effect at spawn; note that but still allow.
		fmt.Fprintf(p.Out, "note: Set %s after Spawn only affects later behavior\n", c.SetKey)
	}
	switch c.SetKey {
	case "Size":
		p.cols, _ = strconv.Atoi(c.SetArgs[0])
		p.rows, _ = strconv.Atoi(c.SetArgs[1])
	case "Term":
		p.term = c.SetArgs[0]
	case "Env":
		p.env = append(p.env, c.SetArgs[0])
	case "WaitTimeout":
		d, _ := time.ParseDuration(c.SetArgs[0])
		p.waitTimeout = d
	case "StabilizeInterval":
		d, _ := time.ParseDuration(c.SetArgs[0])
		p.stabilize = d
	}
	return nil
}

func (p *Player) spawn(c Command) error {
	if p.tt != nil {
		_ = p.tt.Close()
	}
	opts := []tuitest.Option{
		tuitest.WithSize(p.cols, p.rows),
		tuitest.WithTerm(p.term),
	}
	if len(p.env) > 0 {
		opts = append(opts, tuitest.WithEnv(p.env...))
	}
	if p.stabilize > 0 {
		opts = append(opts, tuitest.WithStabilizeInterval(p.stabilize))
	}
	if p.Out != nil && p.Out != io.Discard {
		opts = append(opts, tuitest.WithLog(p.Out))
	}
	if p.Mirror != nil {
		opts = append(opts, tuitest.WithOutputMirror(p.Mirror))
	}
	tt, err := tuitest.Start(c.Argv, opts...)
	if err != nil {
		return err
	}
	p.tt = tt
	return nil
}

func (p *Player) sendKeys(tokens []string) error {
	items := make([]any, 0, len(tokens))
	for _, tok := range tokens {
		k, err := ResolveKey(tok)
		if err != nil {
			return err
		}
		items = append(items, k)
	}
	return p.tt.SendKeys(items...)
}

func (p *Player) timeout(c Command) time.Duration {
	if c.HasTimeout {
		return c.Timeout
	}
	return p.waitTimeout
}

func (p *Player) expect(c Command) error {
	sc := p.tt.Screen()
	hay := sc.Text()
	if c.Scope == tuitest.ScopeLastLine {
		hay = lastNonBlank(sc)
	}
	if !c.Regex.MatchString(hay) {
		return &ExpectError{Regex: c.Regex.String(), Scope: c.Scope, Screen: sc.Text()}
	}
	return nil
}

func lastNonBlank(sc tuitest.Screen) string {
	_, rows := sc.Size()
	for row := rows - 1; row >= 0; row-- {
		if line := sc.Line(row); line != "" {
			return line
		}
	}
	return ""
}

func (p *Player) expectExit(c Command) error {
	code, err := p.tt.Wait(p.waitTimeout)
	if err != nil {
		return err
	}
	if code != c.Code {
		return fmt.Errorf("ExpectExit %d but child exited with %d", c.Code, code)
	}
	return nil
}

func (p *Player) snapshot(c Command) error {
	if p.hidden {
		return nil
	}
	var got string
	if c.Styled {
		got = p.tt.SnapshotStyled()
	} else {
		got = p.tt.Snapshot()
	}
	path := filepath.Join(p.GoldenDir, c.Name+".golden")
	if p.Update {
		if err := os.MkdirAll(p.GoldenDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(got), 0o644) //nolint:gosec
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read golden %s: %w (set UPDATE_GOLDEN to create it)", path, err)
	}
	if string(wantBytes) != got {
		return &SnapshotError{Name: c.Name, Path: path, Want: string(wantBytes), Got: got}
	}
	return nil
}
