package tape_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

var echoBin string

const fixturePrefix = "tuitest-tape-fixture-"

func TestMain(m *testing.M) {
	// A panicking test kills the process before the cleanup below can run, so
	// sweep anything an earlier crashed run left behind. Only directories older
	// than an hour are removed, so a concurrent run of this package keeps its
	// own fixture.
	sweepStaleFixtures(fixturePrefix, time.Hour)

	dir, err := os.MkdirTemp("", fixturePrefix)
	if err != nil {
		panic(err)
	}
	echoBin = filepath.Join(dir, "echotui")
	build := exec.Command("go", "build", "-o", echoBin, "../testdata/echotui")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building echotui fixture: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestPlayerDrivesFixture(t *testing.T) {
	script := `
Set Size 40 10
Spawn ` + echoBin + `
Wait /ECHOTUI/ +Screen @5s
Type hello
Key Enter
Wait /echo: hello/ +Screen @5s
Expect /echo: hello/ +Screen
`
	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}
	p := tape.NewPlayer()
	if err := p.Run(cmds); err != nil {
		t.Fatal(err)
	}
}

func TestPlayerExpectExit(t *testing.T) {
	script := `
Set Size 40 10
Spawn ` + echoBin + `
Wait /ECHOTUI/ +Screen @5s
Type quit
Key Enter
ExpectExit 0
`
	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}
	if err := tape.NewPlayer().Run(cmds); err != nil {
		t.Fatal(err)
	}
}

func TestPlayerSnapshotGolden(t *testing.T) {
	goldenDir := t.TempDir()
	script := `
Set Size 20 4
Spawn ` + echoBin + `
Wait /ECHOTUI/ +Screen @5s
WaitStable @2s
Snapshot banner
`
	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}

	// First pass creates the golden.
	create := tape.NewPlayer()
	create.GoldenDir = goldenDir
	create.Update = true
	if err := create.Run(cmds); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(goldenDir, "banner.golden")); err != nil {
		t.Fatalf("golden not created: %v", err)
	}

	// Second pass compares against it and must pass.
	check := tape.NewPlayer()
	check.GoldenDir = goldenDir
	if err := check.Run(cmds); err != nil {
		t.Fatalf("golden comparison failed: %v", err)
	}
}

// sweepStaleFixtures removes temp directories with the given prefix that are
// older than maxAge, bounding what a crashed run can leave behind.
func sweepStaleFixtures(prefix string, maxAge time.Duration) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), e.Name()))
	}
}
