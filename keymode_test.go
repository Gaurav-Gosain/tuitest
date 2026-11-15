package tuitest

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// showKeys starts a program that sets a keyboard mode and then prints every
// byte it reads, with control bytes made visible by cat -v. The terminal is
// raw, so each key shows as soon as it arrives.
func showKeys(t *testing.T, setMode string) *Terminal {
	t.Helper()
	term := StartT(t, []string{"/bin/sh", "-c", "stty raw -echo; printf '" + setMode + "'; echo ready; exec cat -v"},
		WithKillGrace(-1))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return term
}

// markers numbers the end marker of each sendAndRead call.
var markers atomic.Int64

// sendAndRead sends keys and returns what the program read, as cat -v shows it.
// Each call ends its keys with a numbered marker, and the result is what the
// program printed between the previous marker and this one.
func sendAndRead(t *testing.T, term *Terminal, keys ...any) string {
	t.Helper()
	marker := fmt.Sprintf("<%d>", markers.Add(1))
	if err := term.SendKeys(append(keys, marker)...); err != nil {
		t.Fatal(err)
	}
	var out string
	if err := term.WaitForDesc("the program to echo "+marker, func(s Screen) bool {
		out = flatText(s)
		return strings.Contains(out, marker)
	}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	out = out[:strings.Index(out, marker)]
	if i := strings.LastIndex(out, ">"); i >= 0 {
		out = out[i+1:]
	} else {
		// The terminal is raw, so the newline after "ready" moved down
		// without a carriage return, and blanks fill the row up to it.
		out = strings.TrimLeft(strings.TrimPrefix(out, "ready"), " ")
	}
	return out
}

// flatText joins the screen rows into one string, so output that wrapped reads
// as it was written.
func flatText(s Screen) string {
	_, rows := s.Size()
	var b strings.Builder
	for row := range rows {
		b.WriteString(strings.TrimRight(s.Line(row), " "))
	}
	return b.String()
}

// A program that pushed kitty keyboard flags gets keys in the kitty protocol,
// the way a kitty, Ghostty or WezTerm user's terminal sends them. Before the
// fix SendKeys read only DECCKM, so Ctrl('b') reached such a program as 0x02,
// a byte no kitty user's terminal sends.
func TestSendKeysFollowsKittyFlags(t *testing.T) {
	term := showKeys(t, `\033[>1u`) // disambiguate
	cases := []struct {
		name string
		keys []any
		want string
	}{
		{"Ctrl+b", []any{Ctrl('b')}, "^[[98;5u"},
		{"Alt+x", []any{Alt('x')}, "^[[120;3u"},
		{"Esc", []any{Esc}, "^[[27u"},
		{"Enter stays legacy", []any{Enter}, "^M"},
		{"Alt+Up", []any{Alt(Up)}, "^[[1;3A"},
		{"Alt+Esc", []any{Alt(Esc)}, "^[[27;3u"},
		{"text stays text", []any{"hi"}, "hi"},
	}
	for _, c := range cases {
		if got := sendAndRead(t, term, c.keys...); got != c.want {
			t.Errorf("%s: the program read %q, want %q", c.name, got, c.want)
		}
	}
}

// tuios sets CSI = 29 ; 1 u in window mode: every key is reported, with its
// alternate and its text. Enter is then a key event too.
func TestSendKeysFollowsKittyReportAllKeys(t *testing.T) {
	term := showKeys(t, `\033[=29;1u`)
	cases := []struct {
		name string
		keys []any
		want string
	}{
		{"Enter", []any{Enter}, "^[[13u"},
		{"Tab", []any{Tab}, "^[[9u"},
		{"Ctrl+b", []any{Ctrl('b')}, "^[[98;5u"},
		{"Up", []any{Up}, "^[[A"},
		{"F1", []any{F1}, "^[[P"},
	}
	for _, c := range cases {
		if got := sendAndRead(t, term, c.keys...); got != c.want {
			t.Errorf("%s: the program read %q, want %q", c.name, got, c.want)
		}
	}
}

// Popping the flags goes back to the legacy encoding.
func TestSendKeysAfterKittyFlagsPop(t *testing.T) {
	term := showKeys(t, `\033[>1u\033[<u`)
	if got := sendAndRead(t, term, Ctrl('b')); got != "^B" {
		t.Fatalf("after the pop the program read %q, want %q", got, "^B")
	}
}

// At modifyOtherKeys level 2 a key with Ctrl or Alt is sent as CSI 27.
func TestSendKeysFollowsModifyOtherKeys(t *testing.T) {
	term := showKeys(t, `\033[>4;2m`)
	if got := sendAndRead(t, term, Ctrl('b')); got != "^[[27;5;98~" {
		t.Fatalf("Ctrl+b: the program read %q, want %q", got, "^[[27;5;98~")
	}
	if got := sendAndRead(t, term, Enter); got != "^M" {
		t.Fatalf("Enter: the program read %q, want %q", got, "^M")
	}
}

// With no mode set, keys are sent as their constants say.
func TestSendKeysLegacyUnchanged(t *testing.T) {
	term := showKeys(t, "")
	if got := sendAndRead(t, term, Ctrl('b'), Esc, Up, Enter); got != "^B^[^[[A^M" {
		t.Fatalf("the program read %q, want %q", got, "^B^[^[[A^M")
	}
}
