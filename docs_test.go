package tuitest_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/cli"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// The documentation states facts that live in code: which files exist, which
// verbs the tape language has, which exit codes the CLI returns. Each of those
// drifted at least once while nothing noticed, so these tests tie the prose to
// the code it describes.

func markdownFiles(t *testing.T) []string {
	t.Helper()
	docs, err := filepath.Glob("docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{"README.md"}, docs...)
}

var (
	mdLink    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	htmlSrc   = regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	mdHeading = regexp.MustCompile(`(?m)^#{1,6} +(.+)$`)
	fenced    = regexp.MustCompile("(?s)```.*?```")
)

// slug is GitHub's anchor for a heading: lower case, punctuation other than
// hyphens and spaces dropped, spaces turned into hyphens.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func anchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range mdHeading.FindAllStringSubmatch(fenced.ReplaceAllString(string(src), ""), -1) {
		out[slug(strings.ReplaceAll(m[1], "`", ""))] = true
	}
	return out
}

// TestDocLinksResolve checks every relative link in the README and docs/
// points at a file that exists, and every #anchor at a heading that exists.
func TestDocLinksResolve(t *testing.T) {
	for _, doc := range markdownFiles(t) {
		src, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		text := fenced.ReplaceAllString(string(src), "")
		var targets []string
		for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
			targets = append(targets, m[1])
		}
		for _, m := range htmlSrc.FindAllStringSubmatch(text, -1) {
			targets = append(targets, m[1])
		}
		for _, target := range targets {
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			file, anchor, _ := strings.Cut(target, "#")
			resolved := doc
			if file != "" {
				resolved = filepath.Join(filepath.Dir(doc), file)
				if _, err := os.Stat(resolved); err != nil {
					t.Errorf("%s links to %s, which does not exist", doc, target)
					continue
				}
			}
			if anchor != "" && strings.HasSuffix(resolved, ".md") && !anchors(t, resolved)[anchor] {
				t.Errorf("%s links to %s, but %s has no heading with that anchor", doc, target, resolved)
			}
		}
	}
}

// TestDocsListEveryTapeVerb checks the two hand-written verb lists: the table
// in docs/tape.md and the sentence in the README, including its count.
func TestDocsListEveryTapeVerb(t *testing.T) {
	var verbs []string
	for k := tape.KindSet; k.Verb() != ""; k++ {
		verbs = append(verbs, k.Verb())
	}

	table, err := os.ReadFile("docs/tape.md")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range verbs {
		if !strings.Contains(string(table), "| `"+v+"` |") {
			t.Errorf("docs/tape.md has no table row for %s", v)
		}
		if !strings.Contains(string(readme), "`"+v+"`") {
			t.Errorf("README.md does not mention the %s verb", v)
		}
	}
	count := "The " + strconv.Itoa(len(verbs)) + " verbs are"
	if !strings.Contains(string(readme), count) {
		t.Errorf("README.md should say %q", count)
	}
	if !strings.Contains(string(table), "There are "+strconv.Itoa(len(verbs))+",") {
		t.Errorf("docs/tape.md should say there are %d verbs", len(verbs))
	}
}

// TestDocsListEveryExitCode checks the exit code tables in docs/cli.md and
// the README have a row for every code the CLI can return.
func TestDocsListEveryExitCode(t *testing.T) {
	codes := []int{cli.ExitOK, cli.ExitAssert, cli.ExitUsage, cli.ExitHarness, cli.ExitTimeout, cli.ExitBlank}
	for _, doc := range []string{"docs/cli.md", "README.md"} {
		src, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range codes {
			if !strings.Contains(string(src), "| "+strconv.Itoa(code)+" |") {
				t.Errorf("%s has no exit code table row for %d", doc, code)
			}
		}
	}
}
