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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/internal/textdist"
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
	// KindMouse sends one mouse event.
	KindMouse
	// KindPaste sends text as a bracketed paste.
	KindPaste
	// KindRaw writes bytes to the child with no interpretation.
	KindRaw
	// KindWaitOutput blocks until the child writes anything, bounded by its
	// timeout. Unlike WaitStable it cannot pass without the child reacting.
	KindWaitOutput

	// kindCount is the number of command kinds, so the tables derived from
	// them can be built by iteration rather than written out a second time.
	kindCount
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
	case KindMouse:
		return "Mouse"
	case KindPaste:
		return "Paste"
	case KindRaw:
		return "Raw"
	case KindWaitOutput:
		return "WaitOutput"
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

	// Mouse
	Mouse tuitest.MouseEvent

	// Paste / Raw carry their payload in Text.
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

	case "WaitOutput":
		c.Kind = KindWaitOutput
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

	case "Mouse":
		c.Kind = KindMouse
		ev, pe := parseMouse(verb, rest)
		if pe != nil {
			return c, pe
		}
		c.Mouse = ev
		return c, nil

	case "Paste", "Raw":
		if verb.text == "Paste" {
			c.Kind = KindPaste
		} else {
			c.Kind = KindRaw
		}
		text, pe := parseQuoted(verb, tail, tailCol)
		if pe != nil {
			return c, pe
		}
		c.Text = text
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
		if alt, ok := suggestVerb(verb.text); ok {
			return c, perr(verb.col, "unknown command %q, did you mean %q?", verb.text, alt)
		}
		return c, perr(verb.col, "unknown command %q (see \"tuitest help run\" for the command list)", verb.text)
	}
}

// verbs lists every spelling parseLine accepts, for the did-you-mean hint. It
// is derived from Kind.Verb() rather than written out again, so a command
// cannot be added to the language and then be missing from the suggestions.
var verbs = func() []string {
	out := make([]string, 0, kindCount)
	for k := Kind(0); k < kindCount; k++ {
		if v := k.Verb(); v != "" {
			out = append(out, v)
		}
	}
	return out
}()

// verbAliases maps a token that is not a command to the command a reader who
// wrote it probably wanted. "Stable" is only meaningful as the argument of
// Wait, and writing it alone on a line is a plausible mistake, so it earns a
// hint rather than a bare "unknown command".
var verbAliases = map[string]string{"Stable": "WaitStable"}

// suggestCandidates is what a misspelling is measured against: the real verbs
// plus the aliases, all lowercased, since tape verbs are capitalised and
// "spawn" and "Spwan" should both find "Spawn".
var suggestCandidates = func() []string {
	out := make([]string, 0, len(verbs)+len(verbAliases))
	for _, v := range verbs {
		out = append(out, strings.ToLower(v))
	}
	for a := range verbAliases {
		out = append(out, strings.ToLower(a))
	}
	sort.Strings(out)
	return out
}()

// suggestVerb finds the command closest to an unrecognised token, resolving
// aliases so the hint names something the reader can actually write. It reports
// no suggestion when the closest match is the token itself, since telling a
// reader to write what they already wrote explains nothing.
func suggestVerb(name string) (string, bool) {
	best, ok := textdist.Closest(strings.ToLower(name), suggestCandidates)
	if !ok {
		return "", false
	}
	for _, v := range verbs {
		if strings.EqualFold(v, best) {
			return v, v != name
		}
	}
	for alias, target := range verbAliases {
		if strings.EqualFold(alias, best) {
			return target, true
		}
	}
	return "", false
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

// parseQuoted reads the Go-quoted string argument of a Paste or Raw line.
// Quoting is what lets these carry arbitrary bytes, including the malformed
// UTF-8 and embedded escapes a fuzz repro needs to replay exactly.
func parseQuoted(verb token, tail string, tailCol int) (string, *ParseError) {
	arg := strings.TrimSpace(tail)
	if arg == "" {
		return "", perr(verb.col, "%s needs a quoted string, such as %s \"hi\"", verb.text, verb.text)
	}
	s, err := strconv.Unquote(arg)
	if err != nil {
		return "", perr(tailCol, "%s argument must be a quoted string: %v", verb.text, err)
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
func parseMouse(verb token, tokens []token) (tuitest.MouseEvent, *ParseError) {
	var ev tuitest.MouseEvent
	if len(tokens) < 4 {
		return ev, perr(verb.col, "Mouse needs an action, button, col, and row")
	}
	action, ok := mouseActions[tokens[0].text]
	if !ok {
		return ev, perr(tokens[0].col, "unknown mouse action %q (want Press, Release, or Move)", tokens[0].text)
	}
	button, ok := mouseButtons[tokens[1].text]
	if !ok {
		return ev, perr(tokens[1].col, "unknown mouse button %q (want Left, Middle, Right, WheelUp, or WheelDown)", tokens[1].text)
	}
	pos := [2]int{}
	for i, what := range []string{"col", "row"} {
		n, err := strconv.Atoi(tokens[2+i].text)
		if err != nil {
			return ev, perr(tokens[2+i].col, "Mouse %s %q is not an integer", what, tokens[2+i].text)
		}
		// Mouse coordinates are zero-based cell positions, unlike the counts
		// that Set Size and Resize take.
		if n < 0 || n >= MaxDimension {
			return ev, perr(tokens[2+i].col, "Mouse %s %d is out of range 0..%d", what, n, MaxDimension-1)
		}
		pos[i] = n
	}
	ev = tuitest.MouseEvent{Col: pos[0], Row: pos[1], Button: button, Action: action}
	for _, tok := range tokens[4:] {
		switch tok.text {
		case "+Ctrl":
			ev.Mods |= tuitest.ModCtrl
		case "+Alt":
			ev.Mods |= tuitest.ModAlt
		case "+Shift":
			ev.Mods |= tuitest.ModShift
		default:
			return ev, perr(tok.col, "unexpected token %q (want +Ctrl, +Alt, or +Shift)", tok.text)
		}
	}
	return ev, nil
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
