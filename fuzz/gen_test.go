package fuzz

import (
	"context"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// sentBytes returns what a command delivers to the program as keyboard input:
// the resolved bytes of a key, or the payload of a text command. Mouse events
// and resizes are left out; they are not keystrokes whatever bytes carry them.
func sentBytes(t *testing.T, c tape.Command) string {
	t.Helper()
	switch c.Kind {
	case tape.KindKey:
		var b strings.Builder
		for _, k := range c.Keys {
			enc, err := tape.ResolveKey(k)
			if err != nil {
				t.Fatalf("the generator emitted a key token that does not resolve: %q: %v", k, err)
			}
			b.WriteString(string(enc))
		}
		return b.String()
	case tape.KindRaw, tape.KindPaste, tape.KindType:
		return c.Text
	}
	return ""
}

// Excluding a key has to stop the program from receiving it, not only stop the
// generator from naming it. Ctrl+c is byte 0x03, and the hostile table sends
// that byte inside "\x01\x02\x03\x04", so a program that quits on Ctrl+c still
// quit under --exclude Ctrl+c. Excluding "q", the example the CLI help gives,
// excluded nothing at all, because q is never a key token here; it only ever
// arrives inside text such as "the quick brown fox".
//
// A key is excluded when it sends exactly the excluded bytes, and text never
// contains them. A key that merely starts with them is a different key: with
// Esc excluded, Up still sends ESC [ A, which no program reads as Esc. Esc is
// covered by TestExcludingEscKeepsEscapeSequences, since ESC in text is
// usually the start of a sequence and not the key.
func TestExcludedKeysAreNeverDelivered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		token string
		bytes string
	}{
		{"Ctrl+c", "\x03"},
		{"q", "q"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			t.Parallel()
			cfg := Config{ActionsPerRun: 200, ExcludeKeys: []string{tc.token}}
			for seed := range uint64(40) {
				for i, c := range newGenerator(cfg, seed).Run([]string{"prog"}) {
					sent := sentBytes(t, c)
					if c.Kind == tape.KindKey && sent != tc.bytes {
						continue
					}
					if strings.Contains(sent, tc.bytes) {
						t.Fatalf("seed %d, command %d sends %q with %s excluded:\n%s",
							seed, i, tc.bytes, tc.token, tape.Sprint([]tape.Command{c}))
					}
				}
			}
		})
	}
}

// Excluding Esc must not turn off escape-sequence fuzzing. ESC also starts
// every sequence the hostile table sends, so removing every 0x1b from text
// turned "\x1b[9999H" and the rest into plain printable text. Only a bare ESC,
// one at the end of a payload or before a control byte, is read as the Esc
// key; that one must never be sent, and every other ESC must survive.
func TestExcludingEscKeepsEscapeSequences(t *testing.T) {
	t.Parallel()
	cfg := Config{ActionsPerRun: 200, ExcludeKeys: []string{"Esc"}}
	sequences := 0
	for seed := range uint64(40) {
		for i, c := range newGenerator(cfg, seed).Run([]string{"prog"}) {
			sent := sentBytes(t, c)
			if c.Kind == tape.KindKey && sent != "\x1b" {
				continue
			}
			for j := 0; j < len(sent); j++ {
				if sent[j] != 0x1b {
					continue
				}
				if j+1 == len(sent) || sent[j+1] < 0x20 {
					t.Fatalf("seed %d, command %d sends a bare Esc with Esc excluded:\n%s",
						seed, i, tape.Sprint([]tape.Command{c}))
				}
				if c.Kind == tape.KindRaw && strings.IndexByte("[]OP_^X", sent[j+1]) >= 0 {
					sequences++
				}
			}
		}
	}
	if sequences == 0 {
		t.Fatal("excluding Esc removed every escape sequence from hostile payloads")
	}
}

// An Alt key that is ESC plus a sequence introducer cannot be removed from
// text without removing the sequences it introduces, so it is excluded as a
// key only. Everything else is still removed from text.
func TestWithoutExcludedKeepsSequenceStarts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		exclude []string
		in      string
		want    string
	}{
		{[]string{"Esc"}, "\x1b[9999H", "\x1b[9999H"},
		{[]string{"Esc"}, "\x1b]0;title\x07", "\x1b]0;title\x07"},
		{[]string{"Esc"}, "a\x1b", "a"},
		{[]string{"Esc"}, "\x1b\x1b[A", "\x1b[A"},
		{[]string{"Esc"}, "\x1b\x03x", "\x03x"},
		{[]string{"Esc"}, "\x1bx", "\x1bx"},
		{[]string{"Alt+["}, "\x1b[A", "\x1b[A"},
		{[]string{"Alt+x"}, "a\x1bxb", "ab"},
		{[]string{"Ctrl+c"}, "\x1b[\x03A", "\x1b[A"},
	}
	for _, tc := range cases {
		g := newGenerator(Config{ExcludeKeys: tc.exclude}, 0)
		if got := g.withoutExcluded(tc.in); got != tc.want {
			t.Errorf("excluding %v: %q became %q, want %q", tc.exclude, tc.in, got, tc.want)
		}
	}
}

// The exclusion is matched on what a key sends, so the spelling a user types
// does not have to match the generator's. Ctrl+C and C+c both send 0x03.
func TestExcludedKeysMatchByEncoding(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"Ctrl+C", "C+c"} {
		cfg := Config{ActionsPerRun: 200, ExcludeKeys: []string{token}}
		for seed := range uint64(40) {
			for _, c := range newGenerator(cfg, seed).Run([]string{"prog"}) {
				if c.Kind == tape.KindKey && sentBytes(t, c) == "\x03" {
					t.Fatalf("seed %d sent Ctrl+c with %s excluded", seed, token)
				}
			}
		}
	}
}

// A token that names no key is an error rather than a silent no-op. Before,
// "--exclude ctrl+c" (modifiers are capitalised) excluded nothing and said
// nothing, and the session quit the program as often as without it.
func TestUnknownExcludedKeyIsAnError(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), Options{
		Argv:       []string{"true"},
		Iterations: 1,
		Gen:        Config{ExcludeKeys: []string{"ctrl+c"}},
	})
	if err == nil {
		t.Fatal("an exclusion that names no key was accepted")
	}
	if !strings.Contains(err.Error(), "ctrl+c") {
		t.Errorf("the error should name the token, got %v", err)
	}
}

// Every token in the generator's pools must resolve, or the key exclusion,
// which matches on resolved bytes, cannot see it.
func TestGeneratorKeyPoolsResolve(t *testing.T) {
	t.Parallel()
	for _, pool := range [][]string{navKeys, functionKeys, ctrlKeys, altKeys} {
		for _, k := range pool {
			if _, err := tape.ResolveKey(k); err != nil {
				t.Errorf("pool key %q does not resolve: %v", k, err)
			}
		}
	}
}

// The same seed and the same configuration must draw the same run, exclusions
// included, or the seed a report prints does not reproduce the report.
func TestGenerationIsDeterministic(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{{}, {ExcludeKeys: []string{"Ctrl+c", "q", "Esc", "Up"}}} {
		for seed := range uint64(20) {
			a := tape.Sprint(newGenerator(cfg, seed).Run([]string{"prog"}))
			b := tape.Sprint(newGenerator(cfg, seed).Run([]string{"prog"}))
			if a != b {
				t.Fatalf("seed %d with %v drew two different runs", seed, cfg.ExcludeKeys)
			}
		}
	}
}
