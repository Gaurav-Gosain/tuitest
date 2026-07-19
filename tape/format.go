package tape

import (
	"fmt"
	"strings"

	"github.com/Gaurav-Gosain/tuitest"
)

// Format renders commands back into tape source. It is the inverse of Parse:
// Parse(Format(cmds)) yields the same commands. Generated tapes (notably fuzz
// repros) go through here so the grammar has exactly one writer, and so a
// repro is guaranteed to be a tape the parser accepts.
func Format(cmds []Command) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(FormatCommand(c))
		b.WriteByte('\n')
	}
	return b.String()
}

// FormatCommand renders one command as a single tape line, without a newline.
func FormatCommand(c Command) string {
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
		return "Wait" + formatWaitLike(c)
	case KindWaitStable:
		return "WaitStable" + formatWaitLike(c)
	case KindWaitOutput:
		return "WaitOutput" + formatWaitLike(c)
	case KindWaitPrompt:
		return "WaitPrompt" + formatWaitLike(c)
	case KindWaitCommand:
		return "WaitCommand" + formatWaitLike(c)
	case KindExpect:
		return "Expect" + formatWaitLike(c)
	case KindExpectExit:
		return fmt.Sprintf("ExpectExit %d", c.Code)
	case KindSnapshot:
		if c.Styled {
			return "Snapshot " + c.Name + " +Styled"
		}
		return "Snapshot " + c.Name
	case KindHide:
		return "Hide"
	case KindShow:
		return "Show"
	case KindSleep:
		return "Sleep " + c.Dur.String()
	case KindResize:
		return fmt.Sprintf("Resize %d %d", c.Cols, c.Rows)
	case KindMouse:
		return formatMouse(c.Mouse)
	case KindPaste:
		return "Paste " + Quote(c.Text)
	case KindRaw:
		return "Raw " + Quote(c.Text)
	default:
		return fmt.Sprintf("# unknown command kind %d", c.Kind)
	}
}

func formatWaitLike(c Command) string {
	var b strings.Builder
	if c.HasRegex {
		b.WriteString(" /" + c.Regex.String() + "/")
	}
	if c.Scope == tuitest.ScopeLastLine {
		b.WriteString(" +Line")
	}
	if c.HasTimeout {
		b.WriteString(" @" + c.Timeout.String())
	}
	return b.String()
}

var (
	mouseButtonNames = reverseButtons()
	mouseActionNames = reverseActions()
)

func reverseButtons() map[tuitest.MouseButton]string {
	out := make(map[tuitest.MouseButton]string, len(mouseButtons))
	for name, b := range mouseButtons {
		out[b] = name
	}
	return out
}

func reverseActions() map[tuitest.MouseAction]string {
	out := make(map[tuitest.MouseAction]string, len(mouseActions))
	for name, a := range mouseActions {
		out[a] = name
	}
	return out
}

func formatMouse(ev tuitest.MouseEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mouse %s %s %d %d", mouseActionNames[ev.Action], mouseButtonNames[ev.Button], ev.Col, ev.Row)
	// Emit modifiers in a fixed order so formatting is deterministic.
	if ev.Mods&tuitest.ModCtrl != 0 {
		b.WriteString(" +Ctrl")
	}
	if ev.Mods&tuitest.ModAlt != 0 {
		b.WriteString(" +Alt")
	}
	if ev.Mods&tuitest.ModShift != 0 {
		b.WriteString(" +Shift")
	}
	return b.String()
}
