package cli

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// TestRunHelpListsEveryTapeVerb guards the one place the command line restates
// the tape grammar. `tuitest help run` prints a "Commands:" line, and it drifted
// once already: five verbs were added to the language and the help text kept
// naming fourteen. Anything that documents a list by hand needs a test that the
// list is complete, so this walks every Kind and asserts its canonical spelling
// appears.
//
// Verified to fail on broken code: dropping a verb from the Long text of
// runCommand makes this fail and names the missing one.
func TestRunHelpListsEveryTapeVerb(t *testing.T) {
	t.Parallel()

	long := runCommand().Long
	commands, ok := verbSection(long)
	if !ok {
		t.Fatalf("run help no longer has a Commands: section:\n%s", long)
	}

	for k := tape.KindSet; ; k++ {
		verb := k.Verb()
		if verb == "" {
			break
		}
		if !containsVerb(commands, verb) {
			t.Errorf("`tuitest help run` does not list the %q verb:\n%s", verb, commands)
		}
	}
}

// verbSection returns the text of the "Commands:" paragraph in a help string.
func verbSection(long string) (string, bool) {
	i := strings.Index(long, "Commands:")
	if i < 0 {
		return "", false
	}
	rest := long[i:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		rest = rest[:end]
	}
	return rest, true
}

// containsVerb reports whether the help text names verb as a whole word, so
// that "Wait" does not count as listing "WaitStable".
func containsVerb(text, verb string) bool {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == '\n' || r == '\t'
	}) {
		if field == verb {
			return true
		}
	}
	return false
}
