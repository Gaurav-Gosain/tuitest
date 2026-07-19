package tape

import (
	"strings"
	"testing"
)

// TestEveryVerbIsSuggestible keeps the did-you-mean list from drifting out of
// step with the commands the parser actually accepts. The two used to be
// written out separately, so adding a command meant remembering to add it in a
// second place or it would never be suggested.
func TestEveryVerbIsSuggestible(t *testing.T) {
	suggestible := map[string]bool{}
	for _, v := range verbs {
		suggestible[v] = true
	}
	for k := Kind(0); k < kindCount; k++ {
		v := k.Verb()
		if v == "" {
			t.Fatalf("Kind(%d) has no verb but is below kindCount", int(k))
		}
		if !suggestible[v] {
			t.Errorf("%q is a real command but would never be suggested", v)
		}
	}
}

// TestStableAloneSuggestsWaitStable covers the mistake the verb list was
// documented to catch: "Stable" is only meaningful as the argument of Wait, so
// a reader who writes it alone on a line should be pointed at WaitStable rather
// than told the command list exists.
func TestStableAloneSuggestsWaitStable(t *testing.T) {
	for _, in := range []string{"Stable", "stable", "Stbale"} {
		got, ok := suggestVerb(in)
		if !ok || got != "WaitStable" {
			t.Errorf("suggestVerb(%q) = %q, %v; want \"WaitStable\", true", in, got, ok)
		}
	}
}

// TestSuggestionIsNeverTheWordItself guards the degenerate hint. Suggesting the
// exact token the reader already typed tells them nothing about what is wrong.
func TestSuggestionIsNeverTheWordItself(t *testing.T) {
	for _, in := range []string{"Stable", "Frobnicate", "Wat"} {
		if got, ok := suggestVerb(in); ok && got == in {
			t.Errorf("suggestVerb(%q) suggested the same word back", in)
		}
	}
}

// TestUnknownVerbErrorSuggests is the end-to-end view: the hint has to reach the
// parse error a reader actually sees.
func TestUnknownVerbErrorSuggests(t *testing.T) {
	_, err := Parse(strings.NewReader("Stable\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), "WaitStable") {
		t.Errorf("error should point at WaitStable, got: %v", err)
	}
}
