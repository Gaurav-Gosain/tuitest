package tape

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The tape language is documented by hand in docs/tape.md and summarised in the
// README, and both drifted: the README counted 19 verbs and left out Focus after
// it was added, and docs/tape.md named a mouse button, Backward, that the parser
// rejects. These tests tie the prose to the tables the parser dispatches on.
//
// Verified to fail on the documents as they were: the README count and list,
// and the Backward button.

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDocsListEveryVerb(t *testing.T) {
	var all []string
	for k := Kind(0); k < kindCount; k++ {
		all = append(all, k.Verb())
	}
	count := strconv.Itoa(len(all))

	tapeDoc := readDoc(t, "../docs/tape.md")
	if !strings.Contains(tapeDoc, "There are "+count+",") {
		t.Errorf("docs/tape.md does not say there are %s verbs", count)
	}
	for _, v := range all {
		if !strings.Contains(tapeDoc, "| `"+v+"` |") {
			t.Errorf("docs/tape.md has no table row for %s", v)
		}
	}

	readme := readDoc(t, "../README.md")
	if n := strings.Count(readme, "language of "+count+" verbs"); n != 1 {
		t.Errorf("README does not describe the language as having %s verbs", count)
	}
	i := strings.Index(readme, "The "+count+" verbs are ")
	if i < 0 {
		t.Fatalf("README has no sentence listing the %s verbs", count)
	}
	list := readme[i:]
	list = list[:strings.Index(list, ".")]
	for _, v := range all {
		if !strings.Contains(list, "`"+v+"`") {
			t.Errorf("README verb list leaves out %s: %s", v, list)
		}
	}
}

func TestDocsNameTheRealMouseButtons(t *testing.T) {
	doc := readDoc(t, "../docs/tape.md")
	i := strings.Index(doc, "The button is `")
	if i < 0 {
		t.Fatal("docs/tape.md no longer lists the mouse buttons")
	}
	sentence := doc[i:]
	sentence = sentence[:strings.Index(sentence, ".")]
	var documented []string
	for _, m := range regexp.MustCompile("`([A-Za-z]+)`").FindAllStringSubmatch(sentence, -1) {
		documented = append(documented, m[1])
	}
	var real []string
	for name := range mouseButtons {
		real = append(real, name)
	}
	sort.Strings(documented)
	sort.Strings(real)
	if strings.Join(documented, " ") != strings.Join(real, " ") {
		t.Errorf("docs/tape.md lists mouse buttons %v, the parser accepts %v", documented, real)
	}
}
