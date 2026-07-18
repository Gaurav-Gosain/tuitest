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

	"github.com/Gaurav-Gosain/tuitest"
)

// Kind is a tape command verb.
type Kind int

const (
	KindSet Kind = iota
	KindSpawn
	KindType
	KindKey
	KindWait
	KindWaitStable
	KindWaitPrompt
	KindWaitCommand
	KindExpect
	KindExpectExit
	KindSnapshot
	KindHide
	KindShow
	KindSleep
)

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
		raw := sc.Text()
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
	fields := strings.Fields(raw)
	verb := fields[0]
	rest := fields[1:]
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
		// Preserve literal spacing after "Type ".
		c.Text = strings.TrimPrefix(raw, verbPrefix(raw, "Type"))
		return c, nil

	case "Key":
		if len(rest) == 0 {
			return c, fmt.Errorf("Key needs at least one key name")
		}
		c.Kind = KindKey
		c.Keys = rest
		return c, validateKeys(rest)

	case "Wait":
		if len(rest) == 1 && rest[0] == "Stable" {
			c.Kind = KindWaitStable
			return c, nil
		}
		c.Kind = KindWait
		return c, parseWaitLike(&c, rest)

	case "WaitStable":
		c.Kind = KindWaitStable
		return c, parseWaitLike(&c, rest)

	case "WaitPrompt":
		c.Kind = KindWaitPrompt
		return c, parseWaitLike(&c, rest)

	case "WaitCommand":
		c.Kind = KindWaitCommand
		return c, parseWaitLike(&c, rest)

	case "Expect":
		c.Kind = KindExpect
		if err := parseWaitLike(&c, rest); err != nil {
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
			if tok == "+Styled" {
				c.Styled = true
			}
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
		d, err := time.ParseDuration(rest[0])
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

// verbPrefix returns the leading whitespace + verb + one space, so the rest of a
// Type line keeps its literal internal spacing.
func verbPrefix(raw, verb string) string {
	idx := strings.Index(raw, verb)
	return raw[:idx+len(verb)+1]
}

// parseWaitLike parses the optional /regex/, +Scope, and @timeout tokens shared
// by Wait, Expect, and the semantic waits.
func parseWaitLike(c *Command, tokens []string) error {
	var regexParts []string
	inRegex := false
	for _, tok := range tokens {
		switch {
		case inRegex:
			regexParts = append(regexParts, tok)
			if strings.HasSuffix(tok, "/") {
				inRegex = false
			}
		case strings.HasPrefix(tok, "/"):
			regexParts = append(regexParts, tok)
			if len(tok) > 1 && strings.HasSuffix(tok, "/") {
				inRegex = false
			} else {
				inRegex = true
			}
		case tok == "+Screen":
			c.Scope = tuitest.ScopeScreen
		case tok == "+Line":
			c.Scope = tuitest.ScopeLastLine
		case strings.HasPrefix(tok, "@"):
			d, err := time.ParseDuration(strings.TrimPrefix(tok, "@"))
			if err != nil {
				return fmt.Errorf("timeout %q: %w", tok, err)
			}
			c.Timeout = d
			c.HasTimeout = true
		default:
			return fmt.Errorf("unexpected token %q", tok)
		}
	}
	if len(regexParts) > 0 {
		joined := strings.Join(regexParts, " ")
		body := strings.TrimSuffix(strings.TrimPrefix(joined, "/"), "/")
		re, err := regexp.Compile(body)
		if err != nil {
			return fmt.Errorf("regex %q: %w", body, err)
		}
		c.Regex = re
		c.HasRegex = true
	}
	return nil
}

func validateSet(c Command) error {
	switch c.SetKey {
	case "Size":
		if len(c.SetArgs) != 2 {
			return fmt.Errorf("Set Size needs cols and rows")
		}
		if _, err := strconv.Atoi(c.SetArgs[0]); err != nil {
			return fmt.Errorf("Set Size cols: %w", err)
		}
		if _, err := strconv.Atoi(c.SetArgs[1]); err != nil {
			return fmt.Errorf("Set Size rows: %w", err)
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
		if _, err := time.ParseDuration(c.SetArgs[0]); err != nil {
			return fmt.Errorf("Set %s: %w", c.SetKey, err)
		}
	default:
		return fmt.Errorf("unknown Set key %q", c.SetKey)
	}
	return nil
}
