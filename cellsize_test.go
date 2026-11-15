package tuitest

import (
	"strings"
	"testing"
	"time"
)

// answers starts a program that asks the terminal for its device attributes,
// its size in pixels and its cell size, and prints the answers.
func answers(t *testing.T, opts ...Option) string {
	t.Helper()
	all := append([]Option{WithKillGrace(-1)}, opts...)
	term := StartT(t, []string{"/bin/sh", "-c", "stty raw -echo; printf '\\033[c\\033[14t\\033[16t'; echo; exec cat -v"}, all...)
	if err := term.WaitForDesc("the three answers", func(s Screen) bool {
		return strings.Count(s.Text(), "^[[") >= 3
	}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(term.Screen().Text())
}

func TestWithCellSizeIsReported(t *testing.T) {
	got := answers(t, WithCellSize(8, 16))
	if !strings.Contains(got, "^[[4;384;640t") || !strings.Contains(got, "^[[6;16;8t") {
		t.Fatalf("answers %q, want a 640x384 window and an 8x16 cell", got)
	}
}

func TestWithSixelIsAdvertised(t *testing.T) {
	plain := answers(t)
	if strings.Contains(plain, ";4;") || strings.Contains(plain, ";4c") {
		t.Fatalf("DA1 without WithSixel is %q, want no attribute 4", plain)
	}
	got := answers(t, WithSixel())
	da1 := got[strings.Index(got, "^[[?"):]
	da1 = da1[:strings.IndexByte(da1, 'c')+1]
	if !strings.Contains(da1, ";4;") && !strings.HasSuffix(da1, ";4c") {
		t.Fatalf("DA1 with WithSixel is %q, want attribute 4", da1)
	}
}
