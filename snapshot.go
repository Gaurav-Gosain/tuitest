package tuitest

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Snapshot returns the current plain-text screen.
func (t *Terminal) Snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked().Text()
}

// SnapshotStyled returns the styled, diff-friendly encoding of the screen: each
// row's plain text followed by indented attribute runs for spans that differ
// from the default style. A screen with no styling degrades to the plain form.
func (t *Terminal) SnapshotStyled() string {
	t.mu.Lock()
	snap := t.snapshotLocked()
	t.mu.Unlock()
	return styledEncode(snap)
}

func styledEncode(s *screenSnapshot) string {
	type rowOut struct {
		text string
		runs []string
	}
	rows := make([]rowOut, s.rows)
	lastMeaningful := -1

	for row := 0; row < s.rows; row++ {
		text := s.Line(row)
		var runs []string

		startCol := -1
		curAttr := ""
		flush := func(endCol int) {
			if startCol >= 0 && curAttr != "" {
				runs = append(runs, fmt.Sprintf("%d-%d %s", startCol, endCol, curAttr))
			}
			startCol = -1
			curAttr = ""
		}
		for col := 0; col < s.cols; col++ {
			attr := cellAttrs(s.Cell(col, row))
			if attr != curAttr {
				flush(col - 1)
				if attr != "" {
					startCol = col
					curAttr = attr
				}
			}
		}
		flush(s.cols - 1)

		rows[row] = rowOut{text: text, runs: runs}
		if text != "" || len(runs) > 0 {
			lastMeaningful = row
		}
	}

	var b strings.Builder
	for row := 0; row <= lastMeaningful; row++ {
		b.WriteString(rows[row].text)
		b.WriteByte('\n')
		for _, r := range rows[row].runs {
			b.WriteString("    ")
			b.WriteString(r)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func cellAttrs(c Cell) string {
	if c.Width == 0 {
		return ""
	}
	var toks []string
	if c.Bold {
		toks = append(toks, "b")
	}
	if c.Faint {
		toks = append(toks, "f")
	}
	if c.Conceal {
		// Without this a concealed run and a plain run encode identically, so a
		// golden could not tell "hidden" from "shown".
		toks = append(toks, "c")
	}
	if c.Italic {
		toks = append(toks, "i")
	}
	if c.Underline {
		toks = append(toks, "u")
	}
	if c.Reverse {
		toks = append(toks, "r")
	}
	if c.Strikethrough {
		toks = append(toks, "s")
	}
	if c.Blink {
		toks = append(toks, "k")
	}
	if c.Width == 2 {
		toks = append(toks, "w")
	}
	if spec := colorSpec(c.Fg); spec != "default" {
		toks = append(toks, "fg:"+spec)
	}
	if spec := colorSpec(c.Bg); spec != "default" {
		toks = append(toks, "bg:"+spec)
	}
	return strings.Join(toks, ",")
}

func colorSpec(c Color) string {
	switch c.Kind {
	case ColorIndexed:
		return fmt.Sprintf("%d", c.Index)
	case ColorRGB:
		return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	default:
		return "default"
	}
}

// AssertGolden compares the plain-text snapshot against testdata/<name>.golden,
// failing the test on mismatch with a unified diff. When UPDATE_GOLDEN is set in
// the environment or -update is passed, it rewrites the golden instead.
func (t *Terminal) AssertGolden(tb testing.TB, name string) {
	tb.Helper()
	assertGolden(tb, name, t.Snapshot())
}

// AssertGoldenStyled is AssertGolden for the styled encoding.
func (t *Terminal) AssertGoldenStyled(tb testing.TB, name string) {
	tb.Helper()
	assertGolden(tb, name, t.SnapshotStyled())
}

func assertGolden(tb testing.TB, name, got string) {
	tb.Helper()
	path := filepath.Join("testdata", name+".golden")
	if shouldUpdate() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("tuitest: golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil { //nolint:gosec
			tb.Fatalf("tuitest: write golden: %v", err)
		}
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("tuitest: read golden %s: %v (set UPDATE_GOLDEN to create it)", path, err)
	}
	want := string(wantBytes)
	if want != got {
		tb.Errorf("tuitest: golden %s mismatch:\n%s", path, unifiedDiff(want, got))
	}
}

func shouldUpdate() bool {
	if os.Getenv("UPDATE_GOLDEN") != "" {
		return true
	}
	if f := flag.Lookup("update"); f != nil {
		if g, ok := f.Value.(flag.Getter); ok {
			if b, ok := g.Get().(bool); ok && b {
				return true
			}
		}
	}
	return false
}

// Diff returns a compact line-oriented diff of want versus got, the same
// encoding AssertGolden uses in its failure messages. It is exported so
// out-of-package golden runners (such as the tape player) can reuse it.
func Diff(want, got string) string { return unifiedDiff(want, got) }

// unifiedDiff produces a compact line-oriented diff (want as the baseline)
// computed in-process, so goldens diff without shelling out to system diff.
func unifiedDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	lcs := lcsLines(wl, gl)

	var b strings.Builder
	i, j := 0, 0
	for _, common := range lcs {
		for i < len(wl) && wl[i] != common {
			fmt.Fprintf(&b, "- %s\n", wl[i])
			i++
		}
		for j < len(gl) && gl[j] != common {
			fmt.Fprintf(&b, "+ %s\n", gl[j])
			j++
		}
		fmt.Fprintf(&b, "  %s\n", common)
		i++
		j++
	}
	for ; i < len(wl); i++ {
		fmt.Fprintf(&b, "- %s\n", wl[i])
	}
	for ; j < len(gl); j++ {
		fmt.Fprintf(&b, "+ %s\n", gl[j])
	}
	return strings.TrimRight(b.String(), "\n")
}

func lcsLines(a, b []string) []string {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}
