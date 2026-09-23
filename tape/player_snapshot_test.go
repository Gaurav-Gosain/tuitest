package tape_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// The parser refuses a Snapshot name that leaves the golden directory, but a
// driver such as the fuzzer builds Commands in code and hands them straight to
// Exec. The player has to refuse the name too, or -update writes wherever the
// name points.
//
// Verified to fail without the fix: the player writes escape.golden next to
// the golden directory.
func TestPlayerRefusesSnapshotOutsideGoldenDir(t *testing.T) {
	root := t.TempDir()
	p := tape.NewPlayer()
	p.GoldenDir = filepath.Join(root, "golden")
	p.Update = true
	t.Cleanup(func() { _ = p.Close() })

	if err := p.Exec(tape.Command{Kind: tape.KindSpawn, Argv: []string{echoBin}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Exec(tape.Command{Kind: tape.KindSnapshot, Name: "../escape"}); err == nil {
		t.Error("the player accepted a snapshot name outside the golden directory")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.golden")); err == nil {
		t.Error("the player wrote a golden outside the golden directory")
	}
}

// A Snapshot name may contain a slash to group goldens. With -update the player
// created only the golden directory itself, so the first write into a
// subdirectory failed with "no such file or directory".
//
// Verified to fail without the fix.
func TestPlayerUpdateCreatesGoldenSubdirectory(t *testing.T) {
	goldenDir := filepath.Join(t.TempDir(), "golden")
	cmds, err := tape.Parse(strings.NewReader("Spawn " + echoBin + "\nWait /ECHOTUI/ @5s\nSnapshot group/banner\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := tape.NewPlayer()
	p.GoldenDir = goldenDir
	p.Update = true
	if err := p.Run(cmds); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(goldenDir, "group", "banner.golden")); err != nil {
		t.Fatalf("golden not created: %v", err)
	}
}

// A Set command built in code skips the parser's validation. The player used to
// index its arguments blindly, so a Set Size with no arguments panicked the
// whole run instead of failing the command.
//
// Verified to fail without the fix: the Exec call panics with an index out of
// range.
func TestPlayerRejectsMalformedSetCommand(t *testing.T) {
	for _, c := range []tape.Command{
		{Kind: tape.KindSet, SetKey: "Size"},
		{Kind: tape.KindSet, SetKey: "WaitTimeout", SetArgs: []string{"-1s"}},
		{Kind: tape.KindSet, SetKey: "Bogus", SetArgs: []string{"x"}},
	} {
		if err := tape.NewPlayer().Exec(c); err == nil {
			t.Errorf("Exec(%s) succeeded, want an error", c)
		}
	}
}
