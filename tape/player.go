package tape

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

	// The Override fields are command-line settings that win over the tape's
	// own Set lines: an explicit flag should beat the file it is pointed at.
	// A zero value means "not overridden", so the tape keeps control.
	OverrideCols, OverrideRows int
	OverrideTerm               string
	OverrideWaitTimeout        time.Duration
	// ExtraEnv is added to whatever the tape sets, since environment entries
	// accumulate rather than replace.
	ExtraEnv []string

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
	defer func() { _ = p.Close() }()
	p.applyOverrides()
	for _, c := range cmds {
		if p.Before != nil {
			if e := p.Before(c); e != nil {
				return e
			}
		}
		e := p.Exec(c)
		if p.After != nil {
			p.After(c, e)
		}
		if e != nil {
			// A typed LineError, not fmt.Errorf: the CLI recovers the line
			// number and the underlying error separately to classify the
			// exit code and to render the message without a doubled prefix.
			return &LineError{Line: c.Line, Err: e}
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

// Terminal returns the terminal spawned by the last Spawn command, or nil
// before the first one. Callers that drive a tape command by command via Exec
// use it to observe the screen between commands.
func (p *Player) Terminal() *tuitest.Terminal { return p.tt }

// Close tears down the spawned terminal. It is idempotent, and Run calls it
// automatically; drivers that use Exec directly must call it themselves.
func (p *Player) Close() error {
	if p.tt == nil {
		return nil
	}
	tt := p.tt
	p.tt = nil
	return tt.Close()
}

// Exec plays a single command against the player's current state. It exists so
// a driver such as the fuzzer can interleave its own checks between commands
// while still executing them through exactly the same code path that replaying
// a tape file uses.
func (p *Player) Exec(c Command) error {
	switch c.Kind {
	case KindSet:
		return p.applySet(c)
	case KindSpawn:
		return p.spawn(c)
	case KindType:
		return p.needTerm(func() error { return p.tt.Type(c.Text) })
	case KindKey:
		return p.needTerm(func() error { return p.sendKey(c) })
	case KindWait:
		return p.needTerm(func() error {
			if !c.HasRegex {
				return fmt.Errorf("Wait needs a /regex/ (or use Wait Stable)")
			}
			return p.tt.WaitForMatch(c.Regex, c.Scope, p.timeout(c))
		})
	case KindWaitStable:
		return p.needTerm(func() error { return p.tt.WaitStable(p.timeout(c)) })
	case KindWaitOutput:
		return p.needTerm(func() error { return p.tt.WaitForOutput(p.timeout(c)) })
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
	case KindResize:
		return p.needTerm(func() error {
			// Track the size so a later Spawn in the same tape starts here.
			p.cols, p.rows = c.Cols, c.Rows
			return p.tt.Resize(c.Cols, c.Rows)
		})
	case KindMouse:
		return p.needTerm(func() error { return p.tt.SendMouse(c.Mouse) })
	case KindPaste:
		return p.needTerm(func() error { return p.tt.Paste(c.Text) })
	case KindRaw:
		return p.needTerm(func() error { return p.tt.Type(c.Text) })
	case KindHide:
		p.hidden = true
		return nil
	case KindShow:
		p.hidden = false
		return nil
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

// applyOverrides seeds the spawn configuration from the command line before any
// tape line runs. applySet then refuses to let the tape undo an override.
func (p *Player) applyOverrides() {
	if p.OverrideCols > 0 && p.OverrideRows > 0 {
		p.cols, p.rows = p.OverrideCols, p.OverrideRows
	}
	if p.OverrideTerm != "" {
		p.term = p.OverrideTerm
	}
	if p.OverrideWaitTimeout > 0 {
		p.waitTimeout = p.OverrideWaitTimeout
	}
	p.env = append(p.env, p.ExtraEnv...)
}

func (p *Player) applySet(c Command) error {
	if p.tt != nil {
		// Some settings only take effect at spawn; note that but still allow.
		fmt.Fprintf(p.Out, "note: Set %s after Spawn only affects later behavior\n", c.SetKey)
	}
	switch c.SetKey {
	case "Size":
		if p.OverrideCols > 0 && p.OverrideRows > 0 {
			return nil // an explicit -size on the command line wins
		}
		p.cols, _ = strconv.Atoi(c.SetArgs[0])
		p.rows, _ = strconv.Atoi(c.SetArgs[1])
	case "Term":
		if p.OverrideTerm != "" {
			return nil
		}
		p.term = c.SetArgs[0]
	case "Env":
		p.env = append(p.env, c.SetArgs[0])
	case "WaitTimeout":
		if p.OverrideWaitTimeout > 0 {
			return nil
		}
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

// sendKey renders a Key command through the protocol registry and writes the
// bytes. Going through the registry rather than resolving each token
// individually is what lets a Key line carry attributes such as +Release: those
// belong to the whole keypress, and only a protocol that understands them can
// encode them.
func (p *Player) sendKey(c Command) error {
	b, err := encodeCommand(c, Protocols())
	if err != nil {
		return err
	}
	return p.tt.SendKeys(tuitest.Key(b))
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
	scope := "whole screen"
	if c.Scope == tuitest.ScopeLastLine {
		hay = lastNonBlank(sc)
		scope = "last non-blank line"
	}
	if c.Regex.MatchString(hay) {
		return nil
	}
	// One error type carries both audiences: Screen lets replay render the
	// failing frame side by side, Detail carries the headless explanation of
	// which line came closest and where it first differed.
	return &ExpectError{
		Regex:  c.Regex.String(),
		Scope:  c.Scope,
		Line:   c.Line,
		Screen: sc.Text(),
		Want:   fmt.Sprintf("regex /%s/ to match the %s", c.Regex, scope),
		Detail: explainNoMatch(c.Regex.String(), hay, sc.Text()),
	}
}

// explainNoMatch renders why a regex did not match. For a literal pattern it
// finds the closest line on screen and marks the first column that differs,
// which is what actually tells a reader whether they have a typo, a stray
// space, or genuinely the wrong screen. For a non-literal pattern there is no
// meaningful "expected string" to line up against, so it shows the screen.
func explainNoMatch(pattern, haystack, screen string) string {
	var b strings.Builder
	if literal := regexp.QuoteMeta(pattern) == pattern; literal {
		if best, ok := closestLine(haystack, pattern); ok {
			col := firstDiffColumn(pattern, best)
			b.WriteString("  the closest line on screen was:\n")
			fmt.Fprintf(&b, "    want | %s\n", pattern)
			fmt.Fprintf(&b, "    got  | %s\n", best)
			fmt.Fprintf(&b, "         | %s^ first difference at column %d\n", strings.Repeat(" ", col), col+1)
		}
	}
	b.WriteString("--- screen ---\n")
	b.WriteString(screen)
	return b.String()
}

// closestLine picks the line of hay sharing the longest common prefix with
// want, breaking ties toward the line closest in length. It returns false when
// no line shares even one character, in which case pointing at a "closest"
// line would be misleading noise rather than help.
func closestLine(hay, want string) (string, bool) {
	best, bestScore := "", 0
	for _, line := range strings.Split(hay, "\n") {
		score := commonPrefixLen(line, want)
		if score > bestScore || (score == bestScore && score > 0 && absDiff(len(line), len(want)) < absDiff(len(best), len(want))) {
			best, bestScore = line, score
		}
	}
	return best, bestScore > 0
}

func commonPrefixLen(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := 0
	for n < len(ar) && n < len(br) && ar[n] == br[n] {
		n++
	}
	return n
}

// firstDiffColumn returns the zero-based rune column where want and got first
// differ, or the length of the shorter string when one is a prefix of the other.
func firstDiffColumn(want, got string) int { return commonPrefixLen(want, got) }

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
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
	code, err := p.tt.WaitExit(p.waitTimeout)
	if err != nil {
		return err
	}
	if code != c.Code {
		return &AssertionError{
			Op:   "ExpectExit",
			Line: c.Line,
			Want: fmt.Sprintf("exit status %d", c.Code),
			Got:  fmt.Sprintf("exit status %d", code),
		}
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
		return &SnapshotError{Name: c.Name, Path: path, Line: c.Line, Want: string(wantBytes), Got: got}
	}
	return nil
}
