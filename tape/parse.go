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
	KindResize
	KindMouse
	KindPaste
	KindRaw
	KindWaitOutput
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

	// Resize
	Cols, Rows int

	// Mouse
	Mouse tuitest.MouseEvent

	// Paste / Raw carry their payload in Text.
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

	case "WaitOutput":
		c.Kind = KindWaitOutput
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

	case "Resize":
		if len(rest) != 2 {
			return c, fmt.Errorf("Resize needs cols and rows")
		}
		cols, err := strconv.Atoi(rest[0])
		if err != nil {
			return c, fmt.Errorf("Resize cols: %w", err)
		}
		rows, err := strconv.Atoi(rest[1])
		if err != nil {
			return c, fmt.Errorf("Resize rows: %w", err)
		}
		if cols < 1 || rows < 1 {
			return c, fmt.Errorf("Resize needs positive dimensions, got %dx%d", cols, rows)
		}
		c.Kind = KindResize
		c.Cols, c.Rows = cols, rows
		return c, nil

	case "Mouse":
		c.Kind = KindMouse
		ev, err := parseMouse(rest)
		if err != nil {
			return c, err
		}
		c.Mouse = ev
		return c, nil

	case "Paste":
		c.Kind = KindPaste
		s, err := parseQuoted(raw, verb)
		if err != nil {
			return c, err
		}
		c.Text = s
		return c, nil

	case "Raw":
		c.Kind = KindRaw
		s, err := parseQuoted(raw, verb)
		if err != nil {
			return c, err
		}
		c.Text = s
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

// parseQuoted reads the Go-quoted string argument of a Paste or Raw line.
// Quoting is what lets these carry arbitrary bytes, including the malformed
// UTF-8 and embedded escapes a fuzz repro needs to replay exactly.
func parseQuoted(raw, verb string) (string, error) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), verb))
	if arg == "" {
		return "", fmt.Errorf("%s needs a quoted string", verb)
	}
	s, err := strconv.Unquote(arg)
	if err != nil {
		return "", fmt.Errorf("%s argument must be a quoted string: %w", verb, err)
	}
	return s, nil
}

// Quote renders s as the quoted argument for a Paste or Raw line. It is the
// inverse of parseQuoted and is what tape writers use to emit repro scripts.
func Quote(s string) string { return strconv.Quote(s) }

var mouseButtons = map[string]tuitest.MouseButton{
	"Left":      tuitest.MouseLeft,
	"Middle":    tuitest.MouseMiddle,
	"Right":     tuitest.MouseRight,
	"WheelUp":   tuitest.MouseWheelUp,
	"WheelDown": tuitest.MouseWheelDown,
}

var mouseActions = map[string]tuitest.MouseAction{
	"Press":   tuitest.MousePress,
	"Release": tuitest.MouseRelease,
	"Move":    tuitest.MouseMove,
}

// parseMouse reads "Mouse <Action> <Button> <col> <row> [+Ctrl +Alt +Shift]".
func parseMouse(tokens []string) (tuitest.MouseEvent, error) {
	var ev tuitest.MouseEvent
	if len(tokens) < 4 {
		return ev, fmt.Errorf("Mouse needs an action, button, col, and row")
	}
	action, ok := mouseActions[tokens[0]]
	if !ok {
		return ev, fmt.Errorf("unknown mouse action %q", tokens[0])
	}
	button, ok := mouseButtons[tokens[1]]
	if !ok {
		return ev, fmt.Errorf("unknown mouse button %q", tokens[1])
	}
	col, err := strconv.Atoi(tokens[2])
	if err != nil {
		return ev, fmt.Errorf("Mouse col: %w", err)
	}
	row, err := strconv.Atoi(tokens[3])
	if err != nil {
		return ev, fmt.Errorf("Mouse row: %w", err)
	}
	ev = tuitest.MouseEvent{Col: col, Row: row, Button: button, Action: action}
	for _, tok := range tokens[4:] {
		switch tok {
		case "+Ctrl":
			ev.Mods |= tuitest.ModCtrl
		case "+Alt":
			ev.Mods |= tuitest.ModAlt
		case "+Shift":
			ev.Mods |= tuitest.ModShift
		default:
			return ev, fmt.Errorf("unexpected mouse token %q", tok)
		}
	}
	return ev, nil
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
