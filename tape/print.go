package tape

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Gaurav-Gosain/tuitest"
)

// String renders one command back into tape syntax. The result parses to an
// equivalent command, which is what makes a parsed tape round-trip: formatting,
// comments and the "Wait Stable" spelling are normalized, but nothing that
// affects execution is lost.
//
// A Command with a Kind outside the documented set renders as a comment, since
// there is no syntax that would reproduce it.
func (c Command) String() string {
	verb := c.Kind.Verb()
	if verb == "" {
		return "# unrepresentable command kind " + strconv.Itoa(int(c.Kind))
	}

	var b strings.Builder
	b.WriteString(verb)
	switch c.Kind {
	case KindSet:
		writeTokens(&b, append([]string{c.SetKey}, c.SetArgs...))
	case KindSpawn:
		writeTokens(&b, c.Argv)
	case KindType:
		// Type keeps the remainder of the line verbatim, so the separating
		// space is written even when the text is empty.
		b.WriteByte(' ')
		b.WriteString(c.Text)
	case KindKey:
		writeTokens(&b, c.Keys)
	case KindWait, KindWaitStable, KindWaitPrompt, KindWaitCommand, KindExpect:
		writeWaitLike(&b, c)
	case KindExpectExit:
		b.WriteByte(' ')
		b.WriteString(strconv.Itoa(c.Code))
	case KindSnapshot:
		b.WriteByte(' ')
		b.WriteString(c.Name)
		if c.Styled {
			b.WriteString(" +Styled")
		}
	case KindSleep:
		b.WriteByte(' ')
		b.WriteString(c.Dur.String())
	case KindResize:
		b.WriteByte(' ')
		b.WriteString(strconv.Itoa(c.Cols))
		b.WriteByte(' ')
		b.WriteString(strconv.Itoa(c.Rows))
	case KindHide, KindShow:
		// No arguments.
	}
	return b.String()
}

func writeTokens(b *strings.Builder, toks []string) {
	for _, tok := range toks {
		b.WriteByte(' ')
		b.WriteString(tok)
	}
}

// writeWaitLike emits the /regex/, +Scope and @timeout arguments in the order
// parseWaitLike expects: the regex first, so its slashes bound the pattern and
// the option tokens sit outside them.
func writeWaitLike(b *strings.Builder, c Command) {
	if c.HasRegex && c.Regex != nil {
		b.WriteString(" /")
		b.WriteString(c.Regex.String())
		b.WriteByte('/')
	}
	if c.Scope == tuitest.ScopeLastLine {
		b.WriteString(" +Line")
	}
	if c.HasTimeout {
		b.WriteString(" @")
		b.WriteString(c.Timeout.String())
	}
}

// Print writes cmds to w as a tape, one command per line. Parsing the result
// yields the same commands, so Print is usable both as a formatter and as the
// serialization half of a round-trip check.
func Print(w io.Writer, cmds []Command) error {
	bw := bufio.NewWriter(w)
	for _, c := range cmds {
		if _, err := fmt.Fprintln(bw, c.String()); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("tape: print: %w", err)
	}
	return nil
}

// Sprint is Print into a string, for the callers that build a tape in memory
// (record writing a header before the commands, tests comparing source).
func Sprint(cmds []Command) string {
	var b strings.Builder
	// A strings.Builder never fails to write, so the error is not reachable.
	_ = Print(&b, cmds)
	return b.String()
}
