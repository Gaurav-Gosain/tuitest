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

// ParseError is a tape syntax error carrying the exact source position and the
// offending line. Its message renders the line with a caret under the token at
// fault, which is the whole point of a CLI parse error: the reader should not
// have to count columns by hand.
type ParseError struct {
	File string // source name, empty when parsing an unnamed reader
	Line int    // 1-based line number
	Col  int    // 1-based column in runes, 0 when the whole line is at fault
	Text string // the raw source line
	Msg  string // what went wrong
}

func (e *ParseError) Error() string {
	var b strings.Builder
	if e.File != "" {
		fmt.Fprintf(&b, "%s:", e.File)
	}
	fmt.Fprintf(&b, "%d:", e.Line)
	if e.Col > 0 {
		fmt.Fprintf(&b, "%d:", e.Col)
	}
	fmt.Fprintf(&b, " %s", e.Msg)
	if e.Text != "" {
		gutter := strconv.Itoa(e.Line)
		fmt.Fprintf(&b, "\n  %s | %s", gutter, e.Text)
		if e.Col > 0 {
			fmt.Fprintf(&b, "\n  %s | %s^", strings.Repeat(" ", len(gutter)), caretPad(e.Text, e.Col))
		}
	}
	return b.String()
}

// caretPad builds the run of whitespace that places a caret under column col,
// copying tabs through so the caret stays aligned in a tab-indented tape.
func caretPad(text string, col int) string {
	var b strings.Builder
	i := 1
	for _, r := range text {
		if i >= col {
			break
		}
		if r == '\t' {
			b.WriteRune('\t')
		} else {
			b.WriteRune(' ')
		}
		i++
	}
	return b.String()
}

// perr builds a positioned parse error. Parse fills in File, Line, and Text.
func perr(col int, format string, args ...any) *ParseError {
	return &ParseError{Col: col, Msg: fmt.Sprintf(format, args...)}
}

// token is one whitespace-separated field with its 1-based rune column, kept so
// errors can point at the token that actually failed rather than at the line.
type token struct {
	text string
	col  int
}

// splitTokens splits raw on whitespace, recording each token's 1-based rune
// column. It is strings.Fields plus positions.
func splitTokens(raw string) []token {
	var toks []token
	col := 1
	start := -1
	startCol := 0
	for i, r := range raw {
		if unicode.IsSpace(r) {
			if start >= 0 {
				toks = append(toks, token{raw[start:i], startCol})
				start = -1
			}
		} else if start < 0 {
			start, startCol = i, col
		}
		col++
	}
	if start >= 0 {
		toks = append(toks, token{raw[start:], startCol})
	}
	return toks
}

// texts returns just the token strings, for the Command fields that keep them.
func texts(toks []token) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.text
	}
	return out
}

// Parse reads a tape and returns its commands. Syntax errors are *ParseError
// and carry the source line, column, and text.
func Parse(r io.Reader) ([]Command, error) {
	return ParseNamed(r, "")
}

// ParseNamed is Parse with a source name (usually a file path) attached to any
// parse error, so the CLI can print file:line:col.
func ParseNamed(r io.Reader, name string) ([]Command, error) {
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
		cmd, pe := parseLine(raw)
		if pe != nil {
			pe.File, pe.Line, pe.Text = name, lineNo, raw
			return nil, pe
		}
		cmd.Line = lineNo
		cmds = append(cmds, cmd)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return cmds, nil
}

func parseLine(raw string) (Command, *ParseError) {
	toks := splitTokens(raw)
	verb := toks[0]
	rest := toks[1:]
	var c Command

	switch verb.text {
	case "Set":
		if len(rest) < 1 {
			return c, perr(verb.col, "Set needs a key")
		}
		c.Kind = KindSet
		c.SetKey = rest[0].text
		c.SetArgs = texts(rest[1:])
		return c, validateSet(c, rest)

	case "Spawn":
		if len(rest) == 0 {
			return c, perr(verb.col, "Spawn needs a program")
		}
		c.Kind = KindSpawn
		c.Argv = texts(rest)
		return c, nil

	case "Type":
		c.Kind = KindType
		// Preserve literal spacing after "Type ". A bare "Type" types nothing.
		c.Text = typeArg(raw, verb)
		return c, nil

	case "Key":
		if len(rest) == 0 {
			return c, perr(verb.col, "Key needs at least one key name")
		}
		c.Kind = KindKey
		c.Keys = texts(rest)
		return c, validateKeys(rest)

	case "Wait":
		if len(rest) == 1 && rest[0].text == "Stable" {
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
		if pe := parseWaitLike(&c, rest); pe != nil {
			return c, pe
		}
		if !c.HasRegex {
			return c, perr(verb.col, "Expect needs a /regex/")
		}
		return c, nil

	case "ExpectExit":
		if len(rest) != 1 {
			return c, perr(verb.col, "ExpectExit needs an exit code")
		}
		n, err := strconv.Atoi(rest[0].text)
		if err != nil {
			return c, perr(rest[0].col, "ExpectExit code %q is not an integer", rest[0].text)
		}
		c.Kind = KindExpectExit
		c.Code = n
		return c, nil

	case "Snapshot":
		if len(rest) < 1 {
			return c, perr(verb.col, "Snapshot needs a name")
		}
		c.Kind = KindSnapshot
		c.Name = rest[0].text
		for _, tok := range rest[1:] {
			if tok.text != "+Styled" {
				return c, perr(tok.col, "unexpected token %q (Snapshot takes a name and an optional +Styled)", tok.text)
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
			return c, perr(verb.col, "Sleep needs a duration")
		}
		d, err := time.ParseDuration(rest[0].text)
		if err != nil {
			return c, perr(rest[0].col, "Sleep duration %q is not a duration such as 500ms or 2s", rest[0].text)
		}
		c.Kind = KindSleep
		c.Dur = d
		return c, nil

	default:
		return c, perr(verb.col, "unknown command %q", verb.text)
	}
}

// typeArg returns everything after "Type " with its literal internal spacing.
// A bare "Type" with nothing after it yields the empty string rather than
// slicing past the end of the line.
func typeArg(raw string, verb token) string {
	idx := strings.Index(raw, verb.text)
	after := idx + len(verb.text)
	if after >= len(raw) {
		return ""
	}
	// Drop exactly one separating space, keeping any further spacing literal.
	return raw[after+1:]
}

// parseWaitLike parses the optional /regex/, +Scope, and @timeout tokens shared
// by Wait, Expect, and the semantic waits.
func parseWaitLike(c *Command, toks []token) *ParseError {
	var regexParts []string
	regexCol := 0
	inRegex := false
	for _, tok := range toks {
		switch {
		case inRegex:
			regexParts = append(regexParts, tok.text)
			if strings.HasSuffix(tok.text, "/") {
				inRegex = false
			}
		case strings.HasPrefix(tok.text, "/"):
			regexParts = append(regexParts, tok.text)
			regexCol = tok.col
			if len(tok.text) > 1 && strings.HasSuffix(tok.text, "/") {
				inRegex = false
			} else {
				inRegex = true
			}
		case tok.text == "+Screen":
			c.Scope = tuitest.ScopeScreen
		case tok.text == "+Line":
			c.Scope = tuitest.ScopeLastLine
		case strings.HasPrefix(tok.text, "@"):
			d, err := time.ParseDuration(strings.TrimPrefix(tok.text, "@"))
			if err != nil {
				return perr(tok.col, "timeout %q is not a duration such as @5s", tok.text)
			}
			c.Timeout = d
			c.HasTimeout = true
		default:
			return perr(tok.col, "unexpected token %q (want /regex/, +Screen, +Line, or @timeout)", tok.text)
		}
	}
	if inRegex {
		return perr(regexCol, "unterminated /regex/ (missing the closing '/')")
	}
	if len(regexParts) > 0 {
		joined := strings.Join(regexParts, " ")
		body := strings.TrimSuffix(strings.TrimPrefix(joined, "/"), "/")
		re, err := regexp.Compile(body)
		if err != nil {
			return perr(regexCol, "regex %q: %v", body, err)
		}
		c.Regex = re
		c.HasRegex = true
	}
	return nil
}

func validateSet(c Command, args []token) *ParseError {
	key := args[0]
	rest := args[1:]
	switch c.SetKey {
	case "Size":
		if len(rest) != 2 {
			return perr(key.col, "Set Size needs cols and rows")
		}
		for i, what := range []string{"cols", "rows"} {
			n, err := strconv.Atoi(rest[i].text)
			if err != nil {
				return perr(rest[i].col, "Set Size %s %q is not an integer", what, rest[i].text)
			}
			if n <= 0 {
				return perr(rest[i].col, "Set Size %s must be positive, got %d", what, n)
			}
		}
	case "Term":
		if len(rest) != 1 {
			return perr(key.col, "Set Term needs a name")
		}
	case "Env":
		if len(rest) != 1 || !strings.Contains(rest[0].text, "=") {
			col := key.col
			if len(rest) > 0 {
				col = rest[0].col
			}
			return perr(col, "Set Env needs KEY=VALUE")
		}
	case "WaitTimeout", "StabilizeInterval":
		if len(rest) != 1 {
			return perr(key.col, "Set %s needs a duration", c.SetKey)
		}
		if _, err := time.ParseDuration(rest[0].text); err != nil {
			return perr(rest[0].col, "Set %s %q is not a duration such as 5s", c.SetKey, rest[0].text)
		}
	default:
		return perr(key.col, "unknown Set key %q (want Size, Term, Env, WaitTimeout, or StabilizeInterval)", c.SetKey)
	}
	return nil
}

func validateKeys(toks []token) *ParseError {
	for _, tok := range toks {
		if _, err := ResolveKey(tok.text); err != nil {
			return perr(tok.col, "%v", err)
		}
	}
	return nil
}
