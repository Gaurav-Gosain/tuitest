package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// discardEnv is an Env whose streams go nowhere, for the helpers that report
// progress to stderr but are not being tested on that.
func discardEnv() *Env {
	return &Env{Stdout: io.Discard, Stderr: new(bytes.Buffer), Getenv: func(string) string { return "" }}
}

// TestWriteTapeIsParseable checks that the file record writes, header comments
// and all, reads back as the same commands. A header that did not parse would
// make every recording unusable.
func TestWriteTapeIsParseable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.tape")
	cmds := []tape.Command{
		{Kind: tape.KindSet, SetKey: "Size", SetArgs: []string{"80", "24"}},
		{Kind: tape.KindSpawn, Argv: []string{"/bin/prog", "-x"}},
		{Kind: tape.KindType, Text: "hello"},
		{Kind: tape.KindKey, Keys: []string{"Enter"}},
		{Kind: tape.KindWaitStable},
		{Kind: tape.KindExpectExit, Code: 0},
	}

	if err := writeTapeFile(discardEnv(), path, []string{"/bin/prog", "-x"}, cmds); err != nil {
		t.Fatalf("writeTapeFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# Recorded by tuitest record: /bin/prog -x") {
		t.Errorf("missing or wrong header:\n%s", data)
	}

	got, err := tape.Parse(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("the tape record wrote does not parse: %v\n%s", err, data)
	}
	if len(got) != len(cmds) {
		t.Fatalf("parsed %d commands, want %d:\n%s", len(got), len(cmds), data)
	}
	for i := range cmds {
		if got[i].Kind != cmds[i].Kind {
			t.Errorf("command %d is kind %d, want %d", i, got[i].Kind, cmds[i].Kind)
		}
	}
}

// TestWriteGoldens checks that captured screens land in the golden directory
// under the names the tape refers to.
func TestWriteGoldens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "testdata")
	files := map[string]string{"step-01": "first", "step-02": "second"}

	if err := writeGoldens(discardEnv(), dir, files); err != nil {
		t.Fatalf("writeGoldens: %v", err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name+".golden"))
		if err != nil {
			t.Fatalf("reading golden %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("golden %s = %q, want %q", name, got, want)
		}
	}
}

// TestWriteGoldensNoFilesCreatesNothing checks that a recording made without
// -snapshots does not leave an empty directory behind.
func TestWriteGoldensNoFilesCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "should-not-exist")
	if err := writeGoldens(discardEnv(), dir, nil); err != nil {
		t.Fatalf("writeGoldens: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory %s was created for an empty snapshot set", dir)
	}
}
