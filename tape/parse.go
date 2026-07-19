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
	// KindResize changes the terminal size mid-tape.
	KindResize
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
	case KindResize:
		return "Resize"
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

	// Resize
	Cols, Rows int

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
func splitTokens(raw string) []token { return splitTokensAt(raw, 1) }

// splitTokensAt is splitTokens for a fragment whose first rune sits at column
// base of the original line, so tokens carry absolute columns.
func splitTokensAt(raw string, base int) []token {
	var toks []token
	col := base
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
		// bufio.Scanner strips one trailing CR for CRLF files; strip any that
		// remain so a tape written with Windows line endings parses (and
		// re-prints) identically to a Unix one.
		raw := strings.TrimRight(sc.Text(), "\r")
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
	// tail is everything after the verb and its single separating space, with
	// its literal spacing intact. Type keeps it verbatim, and the wait-like
	// verbs need it because a /regex/ may contain spaces that tokenizing loses.
	tail, tailCol := verbTail(raw, verb)
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
		c.Text = tail
		return c, nil

	case "Key":
		if len(rest) == 0 {
			return c, perr(verb.col, "Key needs at least one key name")
		}
		c.Kind = KindKey
		c.Keys = texts(rest)
		return c, validateKeys(rest)

	case "Wait":
		// "Wait Stable" is the spelled-out spelling of WaitStable; the rest of
		// the line is parsed the same either way.
		if len(rest) > 0 && rest[0].text == "Stable" {
			c.Kind = KindWaitStable
			inner, innerCol := verbTail(tail, rest[0])
			return c, parseWaitLike(&c, inner, innerCol)
		}
		c.Kind = KindWait
		return c, parseWaitLike(&c, tail, tailCol)

	case "WaitStable":
		c.Kind = KindWaitStable
		return c, parseWaitLike(&c, tail, tailCol)

	case "WaitPrompt":
		c.Kind = KindWaitPrompt
		return c, parseWaitLike(&c, tail, tailCol)

	case "WaitCommand":
		c.Kind = KindWaitCommand
		return c, parseWaitLike(&c, tail, tailCol)

	case "Expect":
		c.Kind = KindExpect
		if pe := parseWaitLike(&c, tail, tailCol); pe != nil {
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

	case "Resize":
		if len(rest) != 2 {
			return c, perr(verb.col, "Resize needs cols and rows")
		}
		dims := [2]int{}
		for i, what := range []string{"cols", "rows"} {
			n, err := strconv.Atoi(rest[i].text)
			if err != nil {
				return c, perr(rest[i].col, "Resize %s %q is not an integer", what, rest[i].text)
			}
			if n < 1 || n > MaxDimension {
				return c, perr(rest[i].col, "Resize %s %d is out of range 1..%d", what, n, MaxDimension)
			}
			dims[i] = n
		}
		c.Kind = KindResize
		c.Cols, c.Rows = dims[0], dims[1]
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
		d, err := parsePositiveDuration(rest[0].text)
		if err != nil {
			return c, perr(rest[0].col, "Sleep duration %q is not a positive duration such as 500ms or 2s", rest[0].text)
		}
		c.Kind = KindSleep
		c.Dur = d
		return c, nil

	default:
		return c, perr(verb.col, "unknown command %q", verb.text)
	}
}

// verbTail returns everything after the first token of s, dropping exactly one
// separating whitespace rune so the remainder keeps its literal internal
// spacing, together with the 1-based rune column where that remainder starts.
// Columns stay absolute to the original line, so a caret still lands under the
// right character. A line that is only a verb yields an empty remainder rather
// than slicing past the end.
func verbTail(s string, tok token) (string, int) {
	i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsSpace(r) })
	if i < 0 {
		return "", tok.col
	}
	rest := s[i+len(tok.text):]
	col := tok.col + utf8.RuneCountInString(tok.text)
	if rest == "" {
		return "", col
	}
	r, size := utf8.DecodeRuneInString(rest)
	if !unicode.IsSpace(r) {
		return rest, col
	}
	return rest[size:], col + 1
}

// parseWaitLike parses the optional /regex/, +Scope, and @timeout arguments
// shared by Wait, Expect, and the semantic waits. The regex runs from the first
// '/' in the arguments to the last, so a pattern may contain spaces and
// slashes; everything outside that span is whitespace-separated option tokens.
// base is the column of args[0], so errors can still point at a real column.
func parseWaitLike(c *Command, args string, base int) *ParseError {
	before, after := args, ""
	if i := strings.Index(args, "/"); i >= 0 {
		j := strings.LastIndex(args, "/")
		slashCol := base + utf8.RuneCountInString(args[:i])
		if j == i {
			return perr(slashCol, "unterminated /regex/ (missing the closing '/')")
		}
		body := args[i+1 : j]
		re, err := regexp.Compile(body)
		if err != nil {
			return perr(slashCol, "regex %q: %v", body, err)
		}
		c.Regex = re
		c.HasRegex = true
		before, after = args[:i], args[j+1:]
	}

	opts := splitTokensAt(before, base)
	if after != "" {
		afterCol := base + utf8.RuneCountInString(args[:len(args)-len(after)])
		opts = append(opts, splitTokensAt(after, afterCol)...)
	}
	for _, tok := range opts {
		switch {
		case tok.text == "+Screen":
			c.Scope = tuitest.ScopeScreen
		case tok.text == "+Line":
			c.Scope = tuitest.ScopeLastLine
		case strings.HasPrefix(tok.text, "@"):
			d, err := parsePositiveDuration(strings.TrimPrefix(tok.text, "@"))
			if err != nil {
				return perr(tok.col, "timeout %q is not a positive duration such as @5s", tok.text)
			}
			c.Timeout = d
			c.HasTimeout = true
		default:
			return perr(tok.col, "unexpected token %q (want /regex/, +Screen, +Line, or @timeout)", tok.text)
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
			if n < 1 || n > MaxDimension {
				return perr(rest[i].col, "Set Size %s %d is out of range 1..%d", what, n, MaxDimension)
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
		if _, err := parsePositiveDuration(rest[0].text); err != nil {
			return perr(rest[0].col, "Set %s %q is not a positive duration such as 5s", c.SetKey, rest[0].text)
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

// MaxDimension bounds Set Size. A tape is untrusted input to the CLI, and the
// grid it names is allocated up front, so an absurd size has to be rejected at
// parse time rather than turned into a multi-gigabyte allocation.
const MaxDimension = 10000
