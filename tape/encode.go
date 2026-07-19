package tape

import (
	"fmt"
	"strings"

	"github.com/Gaurav-Gosain/tuitest"
)

// Encode renders commands back into tape source. It is the inverse of Parse for
// every command Parse can produce, so a recorded tape can be written out, read
// back, and replayed. The output is the readable form a human would edit: one
// command per line, no redundant tokens.
func Encode(cmds []Command) string {
	var b strings.Builder
	for _, c := range cmds {
		line := EncodeCommand(c)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// EncodeCommand renders a single command as one tape line, without a trailing
// newline. It returns "" for a command it cannot represent.
func EncodeCommand(c Command) string {
	switch c.Kind {
	case KindSet:
		return strings.TrimRight("Set "+c.SetKey+" "+strings.Join(c.SetArgs, " "), " ")
	case KindSpawn:
		return "Spawn " + strings.Join(c.Argv, " ")
	case KindType:
		return "Type " + c.Text
	case KindKey:
		return "Key " + strings.Join(c.Keys, " ")
	case KindWait:
		return "Wait" + waitSuffix(c)
	case KindWaitStable:
		return "WaitStable" + waitSuffix(c)
	case KindWaitPrompt:
		return "WaitPrompt" + waitSuffix(c)
	case KindWaitCommand:
		return "WaitCommand" + waitSuffix(c)
	case KindExpect:
		return "Expect" + waitSuffix(c)
	case KindExpectExit:
		return fmt.Sprintf("ExpectExit %d", c.Code)
	case KindSnapshot:
		if c.Styled {
			return "Snapshot " + c.Name + " +Styled"
		}
		return "Snapshot " + c.Name
	case KindResize:
		return fmt.Sprintf("Resize %d %d", c.Cols, c.Rows)
	case KindHide:
		return "Hide"
	case KindShow:
		return "Show"
	case KindSleep:
		return "Sleep " + c.Dur.String()
	default:
		return ""
	}
}

// waitSuffix renders the optional /regex/, +Scope, and @timeout tokens shared by
// the wait-like commands, in the order Parse accepts them.
func waitSuffix(c Command) string {
	var b strings.Builder
	if c.HasRegex && c.Regex != nil {
		b.WriteString(" /" + c.Regex.String() + "/")
	}
	switch c.Scope {
	case tuitest.ScopeLastLine:
		b.WriteString(" +Line")
	case tuitest.ScopeScreen:
		// The default scope; emitting +Screen is noise in a recorded tape only
		// when there is nothing else on the line, but keeping it explicit next
		// to a regex is how the hand-written tapes read.
		if c.HasRegex {
			b.WriteString(" +Screen")
		}
	}
	if c.HasTimeout {
		b.WriteString(" @" + c.Timeout.String())
	}
	return b.String()
}
