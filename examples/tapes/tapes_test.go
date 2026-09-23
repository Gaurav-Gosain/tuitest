// Package tapes holds example tapes, one or more for every verb of the tape
// language, and the test that runs each of them through the tuitest command
// line exactly as a user would. The programs they drive are small sh scripts
// beside them, so they need nothing installed.
//
// Record the goldens again after changing a tape:
//
//	UPDATE_GOLDEN=1 go test ./examples/tapes/
package tapes

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/cli"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

func exampleTapes(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("*.tape")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no example tapes found: %v", err)
	}
	return paths
}

// TestExampleTapesPass runs every tape as "tuitest run" would, from this
// directory, and requires exit 0. It does not pass --strict, because
// settings.tape shows Sleep on purpose.
func TestExampleTapesPass(t *testing.T) {
	for _, path := range exampleTapes(t) {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			args := []string{"run", path}
			if os.Getenv("UPDATE_GOLDEN") != "" {
				args = []string{"run", "--update", path}
			}
			var stdout, stderr bytes.Buffer
			code := cli.Main(&cli.Env{Stdout: &stdout, Stderr: &stderr, Getenv: os.Getenv}, args)
			if code != cli.ExitOK {
				t.Fatalf("tuitest run %s exited %d\n%s%s", path, code, stdout.String(), stderr.String())
			}
		})
	}
}

// TestExampleTapesCoverEveryVerb keeps the examples complete: a verb added to
// the language without an example here fails this test.
func TestExampleTapesCoverEveryVerb(t *testing.T) {
	used := map[string]bool{}
	for _, path := range exampleTapes(t) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cmds, err := tape.Parse(bytes.NewReader(src))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, c := range cmds {
			used[c.Kind.Verb()] = true
		}
	}
	var missing []string
	for k := tape.KindSet; k.Verb() != ""; k++ {
		if !used[k.Verb()] {
			missing = append(missing, k.Verb())
		}
	}
	if len(missing) > 0 {
		t.Errorf("no example tape uses %s", strings.Join(missing, ", "))
	}
}
