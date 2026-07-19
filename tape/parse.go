// Package tape implements the small VHS-inspired tape language for tuitest's
// CLI and a player that drives a tuitest.Terminal. The grammar is line
// oriented, one command per line, with '#' introducing a comment. It covers
// exactly the harness primitives and is not a reuse of tuios's tape format.
package tape

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Gaurav-Gosain/tuitest"
)

// Kind is a tape command verb.
type Kind int

const (
	// KindSet configures the next Spawn (Size, Term, Env, WaitTimeout,
	// StabilizeInterval).
	KindSet Kind = iota
	// KindSpawn starts the program under test.
	KindSpawn
	// KindType sends the rest of the line literally.
	KindType
	// KindKey sends one or more named keys or chords.
	KindKey
	// KindWait blocks until a regex matches the screen.
	KindWait
	// KindWaitStable blocks until output has quiesced.
	KindWaitStable
	// KindWaitPrompt blocks until a new OSC 133 prompt is drawn.
	KindWaitPrompt
	// KindWaitCommand blocks until an OSC 133 command finishes.
	KindWaitCommand
	// KindExpect asserts a regex matches the current screen without waiting.
	KindExpect
	// KindExpectExit waits for the child to exit and asserts its code.
	KindExpectExit
	// KindSnapshot compares the screen against a golden file.
	KindSnapshot
	// KindHide makes subsequent snapshots no-ops.
	KindHide
	// KindShow undoes KindHide.
	KindShow
	// KindSleep sleeps for a fixed duration.
	KindSleep
)

// Verb returns the canonical spelling of the command verb, the one Print emits.
func (k Kind) Verb() string {
	switch k {
	case KindSet:
		return "Set"
	case KindSpawn:
		return "Spawn"
	case KindType:
		return "Type"
	case KindKey:
		return "Key"
	case KindWait:
		return "Wait"
	case KindWaitStable:
		return "WaitStable"
	case KindWaitPrompt:
		return "WaitPrompt"
	case KindWaitCommand:
		return "WaitCommand"
	case KindExpect:
		return "Expect"
	case KindExpectExit:
		return "ExpectExit"
	case KindSnapshot:
		return "Snapshot"
	case KindHide:
		return "Hide"
	case KindShow:
		return "Show"
	case KindSleep:
		return "Sleep"
	default:
		return ""
	}
}

// String implements fmt.Stringer for diagnostics.
func (k Kind) String() string {
	if v := k.Verb(); v != "" {
		return v
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Command is one parsed tape line.
type Command struct {
	Kind Kind
	Line int

	// Set
	SetKey  string
	SetArgs []string

	// Spawn
	Argv []string

	// Type
	Text string

	// Key
	Keys []string

	// Wait / Expect
	Regex      *regexp.Regexp
	HasRegex   bool
	Scope      tuitest.Scope
	Timeout    time.Duration
	HasTimeout bool

	// ExpectExit
	Code int

	// Snapshot
	Name   string
	Styled bool

	// Sleep
	Dur time.Duration
}

// Parse reads a tape and returns its commands. Errors carry the source line.
func Parse(r io.Reader) ([]Command, error) {
	var cmds []Command
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		// bufio.Scanner strips one trailing CR for CRLF files; strip any that
		// remain so a tape written with Windows line endings parses (and
		// re-prints) identically to a Unix one.
		raw := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cmd, err := parseLine(raw, lineNo)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		cmds = append(cmds, cmd)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return cmds, nil
}

func parseLine(raw string, lineNo int) (Command, error) {
	verb, tail := splitVerb(raw)
	rest := strings.Fields(tail)
	c := Command{Line: lineNo}

	switch verb {
	case "Set":
		if len(rest) < 1 {
			return c, fmt.Errorf("Set needs a key")
		}
		c.Kind = KindSet
		c.SetKey = rest[0]
		c.SetArgs = rest[1:]
		return c, validateSet(c)

	case "Spawn":
		if len(rest) == 0 {
			return c, fmt.Errorf("Spawn needs a program")
		}
		c.Kind = KindSpawn
		c.Argv = rest
		return c, nil

	case "Type":
		c.Kind = KindType
		// Preserve the literal spacing of everything after "Type ".
		c.Text = tail
		return c, nil

	case "Key":
		if len(rest) == 0 {
			return c, fmt.Errorf("Key needs at least one key name")
		}
		c.Kind = KindKey
		c.Keys = rest
		return c, validateKeys(rest)

	case "Wait":
		// "Wait Stable" is the spelled-out spelling of WaitStable; the rest of
		// the line is parsed the same either way.
		if len(rest) > 0 && rest[0] == "Stable" {
			c.Kind = KindWaitStable
			return c, parseWaitLike(&c, strings.TrimPrefix(strings.TrimSpace(tail), "Stable"))
		}
		c.Kind = KindWait
		return c, parseWaitLike(&c, tail)

	case "WaitStable":
		c.Kind = KindWaitStable
		return c, parseWaitLike(&c, tail)

	case "WaitPrompt":
		c.Kind = KindWaitPrompt
		return c, parseWaitLike(&c, tail)

	case "WaitCommand":
		c.Kind = KindWaitCommand
		return c, parseWaitLike(&c, tail)

	case "Expect":
		c.Kind = KindExpect
		if err := parseWaitLike(&c, tail); err != nil {
			return c, err
		}
		if !c.HasRegex {
			return c, fmt.Errorf("Expect needs a /regex/")
		}
		return c, nil

	case "ExpectExit":
		if len(rest) != 1 {
			return c, fmt.Errorf("ExpectExit needs an exit code")
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil {
			return c, fmt.Errorf("ExpectExit code: %w", err)
		}
		c.Kind = KindExpectExit
		c.Code = n
		return c, nil

	case "Snapshot":
		if len(rest) < 1 {
			return c, fmt.Errorf("Snapshot needs a name")
		}
		c.Kind = KindSnapshot
		c.Name = rest[0]
		for _, tok := range rest[1:] {
			if tok != "+Styled" {
				return c, fmt.Errorf("unexpected token %q (only +Styled is allowed)", tok)
			}
			c.Styled = true
		}
		return c, nil

	case "Hide":
		c.Kind = KindHide
		return c, nil

	case "Show":
		c.Kind = KindShow
		return c, nil

	case "Sleep":
		if len(rest) != 1 {
			return c, fmt.Errorf("Sleep needs a duration")
		}
		d, err := parsePositiveDuration(rest[0])
		if err != nil {
			return c, fmt.Errorf("Sleep duration: %w", err)
		}
		c.Kind = KindSleep
		c.Dur = d
		return c, nil

	default:
		return c, fmt.Errorf("unknown command %q", verb)
	}
}

// splitVerb splits a line into its verb and the raw remainder. Exactly one
// whitespace rune is consumed as the separator, so the remainder of a Type line
// keeps its literal internal spacing. A line that is only a verb yields an empty
// remainder rather than panicking on a short slice.
func splitVerb(raw string) (verb, tail string) {
	s := strings.TrimLeftFunc(raw, unicode.IsSpace)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	_, size := utf8.DecodeRuneInString(s[i:])
	return s[:i], s[i+size:]
}

// parseWaitLike parses the optional /regex/, +Scope, and @timeout arguments
// shared by Wait, Expect, and the semantic waits. The regex runs from the first
// '/' on the line to the last one, so a pattern may contain spaces and slashes;
// everything outside that span is whitespace-separated option tokens.
func parseWaitLike(c *Command, tail string) error {
	opts := tail
	if i := strings.Index(tail, "/"); i >= 0 {
		j := strings.LastIndex(tail, "/")
		if j == i {
			return fmt.Errorf("unterminated /regex/ (no closing slash)")
		}
		body := tail[i+1 : j]
		re, err := regexp.Compile(body)
		if err != nil {
			return fmt.Errorf("regex %q: %w", body, err)
		}
		c.Regex = re
		c.HasRegex = true
		opts = tail[:i] + " " + tail[j+1:]
	}
	for _, tok := range strings.Fields(opts) {
		switch {
		case tok == "+Screen":
			c.Scope = tuitest.ScopeScreen
		case tok == "+Line":
			c.Scope = tuitest.ScopeLastLine
		case strings.HasPrefix(tok, "@"):
			d, err := parsePositiveDuration(strings.TrimPrefix(tok, "@"))
			if err != nil {
				return fmt.Errorf("timeout %q: %w", tok, err)
			}
			c.Timeout = d
			c.HasTimeout = true
		default:
			return fmt.Errorf("unexpected token %q", tok)
		}
	}
	return nil
}

// parsePositiveDuration parses a duration and rejects the non-positive ones. A
// zero or negative timeout, sleep, or quiet window is always a mistake in a
// tape, and a negative one would otherwise reach the harness as "no timeout".
func parsePositiveDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be positive", s)
	}
	return d, nil
}

func validateSet(c Command) error {
	switch c.SetKey {
	case "Size":
		if len(c.SetArgs) != 2 {
			return fmt.Errorf("Set Size needs cols and rows")
		}
		if _, err := parseDimension("cols", c.SetArgs[0]); err != nil {
			return err
		}
		if _, err := parseDimension("rows", c.SetArgs[1]); err != nil {
			return err
		}
	case "Term":
		if len(c.SetArgs) != 1 {
			return fmt.Errorf("Set Term needs a name")
		}
	case "Env":
		if len(c.SetArgs) != 1 || !strings.Contains(c.SetArgs[0], "=") {
			return fmt.Errorf("Set Env needs KEY=VALUE")
		}
	case "WaitTimeout", "StabilizeInterval":
		if len(c.SetArgs) != 1 {
			return fmt.Errorf("Set %s needs a duration", c.SetKey)
		}
		if _, err := parsePositiveDuration(c.SetArgs[0]); err != nil {
			return fmt.Errorf("Set %s: %w", c.SetKey, err)
		}
	default:
		return fmt.Errorf("unknown Set key %q", c.SetKey)
	}
	return nil
}

// MaxDimension bounds Set Size. A tape is untrusted input to the CLI, and the
// grid it names is allocated up front, so an absurd size has to be rejected at
// parse time rather than turned into a multi-gigabyte allocation.
const MaxDimension = 10000

func parseDimension(what, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("Set Size %s: %w", what, err)
	}
	if n < 1 || n > MaxDimension {
		return 0, fmt.Errorf("Set Size %s: %d out of range 1..%d", what, n, MaxDimension)
	}
	return n, nil
}
