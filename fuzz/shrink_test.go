package fuzz

import (
	"context"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// shrinkWith runs the minimisation passes against a caller-supplied predicate
// instead of a real program, so the search strategy can be tested on its own
// without spawning anything. It mirrors shrink's structure; the production
// path differs only in that its predicate replays the candidate.
func shrinkWith(cmds []tape.Command, budget int, stillFails func([]tape.Command) bool) []tape.Command {
	opts := Options{ShrinkBudget: budget}.withDefaults()
	f := &Failure{Kind: FailCrash, Commands: cmds}
	return shrinkUsing(context.Background(), opts, f, stillFails).Commands
}

func cmdKey(k string) tape.Command {
	return tape.Command{Kind: tape.KindKey, Keys: []string{k}}
}

func cmdRaw(s string) tape.Command {
	return tape.Command{Kind: tape.KindRaw, Text: s}
}

func hasKey(cmds []tape.Command, want string) bool {
	for _, c := range cmds {
		if c.Kind == tape.KindKey && len(c.Keys) == 1 && c.Keys[0] == want {
			return true
		}
	}
	return false
}

// Verified to fail on broken code: making removeRange a no-op (returning cmds
// unchanged) leaves all 41 commands and the length assertion fails.
func TestShrinkRemovesIrrelevantCommands(t *testing.T) {
	t.Parallel()

	// One needle in a haystack of forty irrelevant keystrokes.
	cmds := []tape.Command{{Kind: tape.KindSpawn, Argv: []string{"prog"}}}
	for i := 0; i < 20; i++ {
		cmds = append(cmds, cmdKey("Up"))
	}
	cmds = append(cmds, cmdKey("F5"))
	for i := 0; i < 20; i++ {
		cmds = append(cmds, cmdKey("Down"))
	}

	got := shrinkWith(cmds, 500, func(candidate []tape.Command) bool {
		return hasKey(candidate, "F5")
	})

	if !hasKey(got, "F5") {
		t.Fatalf("minimisation dropped the command that causes the failure: %v", got)
	}
	// Spawn plus the needle is the ideal; allow a little slack for the chunk
	// schedule, but nothing close to the original 41.
	if len(got) > 4 {
		t.Fatalf("minimised to %d commands, want at most 4:\n%s", len(got), tape.Format(got))
	}
}

// Verified to fail on broken code: removing the `required` guard lets the
// shrinker delete the Spawn, and this assertion fails.
func TestShrinkNeverRemovesTheSpawn(t *testing.T) {
	t.Parallel()

	cmds := []tape.Command{
		{Kind: tape.KindSpawn, Argv: []string{"prog"}},
		cmdKey("Up"),
		cmdKey("Down"),
	}
	// A predicate that is satisfied by anything at all would happily accept an
	// empty candidate, so only the guard keeps the Spawn.
	got := shrinkWith(cmds, 200, func([]tape.Command) bool { return true })

	if len(got) == 0 || got[0].Kind != tape.KindSpawn {
		t.Fatalf("the Spawn must survive minimisation, got:\n%s", tape.Format(got))
	}
}

// Verified to fail on broken code: dropping the prefix ladder from shrinkText
// (returning nil) leaves the payload at its original 400 bytes and the length
// assertion fails.
func TestShrinkSimplifiesPayloadsToTheBytesThatMatter(t *testing.T) {
	t.Parallel()

	cmds := []tape.Command{
		{Kind: tape.KindSpawn, Argv: []string{"prog"}},
		cmdRaw("\x1b[" + strings.Repeat("9", 400)),
	}
	// The failure depends only on the payload starting with an escape.
	got := shrinkWith(cmds, 500, func(candidate []tape.Command) bool {
		for _, c := range candidate {
			if c.Kind == tape.KindRaw && strings.HasPrefix(c.Text, "\x1b") {
				return true
			}
		}
		return false
	})

	var payload string
	for _, c := range got {
		if c.Kind == tape.KindRaw {
			payload = c.Text
		}
	}
	if !strings.HasPrefix(payload, "\x1b") {
		t.Fatalf("minimisation destroyed the payload that causes the failure: %q", payload)
	}
	if len(payload) > 60 {
		t.Fatalf("payload stayed %d bytes; the prefix ladder should have cut it right down", len(payload))
	}
}

// Verified to fail on broken code: making spend always return true and dropping
// the `budget <= 0` loop guards lets minimisation run to exhaustion, which
// takes 8 replays here and fails the assertion. The budget matters because in
// production every candidate costs a full spawn-and-drive.
func TestShrinkRespectsTheBudget(t *testing.T) {
	t.Parallel()

	cmds := []tape.Command{{Kind: tape.KindSpawn, Argv: []string{"prog"}}}
	for i := 0; i < 50; i++ {
		cmds = append(cmds, cmdKey("Up"))
	}

	calls := 0
	shrinkWith(cmds, 7, func([]tape.Command) bool {
		calls++
		return true
	})

	if calls > 7 {
		t.Fatalf("minimisation made %d candidate replays, budget was 7", calls)
	}
}
