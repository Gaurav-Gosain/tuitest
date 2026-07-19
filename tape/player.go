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
	defer func() {
		if p.tt != nil {
			_ = p.tt.Close()
		}
	}()
	p.applyOverrides()
	for _, c := range cmds {
		if e := p.run(c); e != nil {
			return &LineError{Line: c.Line, Err: e}
		}
	}
	return nil
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
	case KindSleep:
		if p.Strict {
			return fmt.Errorf("Sleep is disallowed in strict mode; wait on a condition instead")
		}
		time.Sleep(c.Dur)
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
	scope := "whole screen"
	if c.Scope == tuitest.ScopeLastLine {
		hay = lastNonBlank(sc)
		scope = "last non-blank line"
	}
	if c.Regex.MatchString(hay) {
		return nil
	}
	return &AssertionError{
		Op:     "Expect",
		Line:   c.Line,
		Want:   fmt.Sprintf("regex /%s/ to match the %s", c.Regex, scope),
		Got:    "no match",
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
		return &AssertionError{
			Op:     "Snapshot",
			Line:   c.Line,
			Want:   fmt.Sprintf("the screen to match golden %s", path),
			Got:    "a different screen",
			Detail: tuitest.Diff(string(wantBytes), got),
		}
	}
	return nil
}
