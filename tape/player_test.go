package tape_test

import (
	"errors"
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

// TestPlayerCallsAfterHookForEveryCommand covers the seam replay depends on:
// the Replayer echoes each command and renders the failing frame from the
// After hook, so a hook that never fires would leave replay silent and its
// side-by-side failure output empty.
//
// Verified to fail: guarding the p.After call with a false condition in
// Player.Run makes this test fail with zero commands seen.
func TestPlayerCallsAfterHookForEveryCommand(t *testing.T) {
	cmds, err := tape.Parse(strings.NewReader(
		"Set Size 40 10\nSpawn " + echoBin + "\nWait /ECHOTUI/ @5s\nType hi\n"))
	if err != nil {
		t.Fatal(err)
	}

	var seen []tape.Kind
	p := tape.NewPlayer()
	p.After = func(c tape.Command, err error) { seen = append(seen, c.Kind) }
	if err := p.Run(cmds); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []tape.Kind{tape.KindSet, tape.KindSpawn, tape.KindWait, tape.KindType}
	if len(seen) != len(want) {
		t.Fatalf("After saw %d commands (%v), want %d", len(seen), seen, len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("After command %d was %v, want %v", i, seen[i], want[i])
		}
	}
}

// TestPlayerBeforeHookCanAbort pins that returning an error from Before stops
// the tape, which is how replay's -step mode aborts on request.
//
// Verified to fail: ignoring the Before error in Player.Run lets the whole
// tape run and this test then sees more than one command.
func TestPlayerBeforeHookCanAbort(t *testing.T) {
	cmds, err := tape.Parse(strings.NewReader(
		"Set Size 40 10\nSpawn " + echoBin + "\nWait /ECHOTUI/ @5s\n"))
	if err != nil {
		t.Fatal(err)
	}

	n := 0
	stop := errors.New("stop here")
	p := tape.NewPlayer()
	p.Before = func(c tape.Command) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	}
	if err := p.Run(cmds); !errors.Is(err, stop) {
		t.Fatalf("Run returned %v, want the Before error", err)
	}
	if n != 2 {
		t.Errorf("Before ran %d times, want 2 (the tape did not stop)", n)
	}
}

// TestResizeSetsTheSizeOfALaterSpawn covers the size bookkeeping a Resize does
// beyond resizing the live terminal. A tape that resizes and then spawns again
// must get the resized geometry, or the second program starts at the tape's
// original Set Size and every assertion about its layout is measuring the
// wrong screen.
//
// Verified to fail: dropping the "p.cols, p.rows = c.Cols, c.Rows" assignment
// in the KindResize case makes the second program report 40 columns.
func TestResizeSetsTheSizeOfALaterSpawn(t *testing.T) {
	// stty reads the geometry from the tty itself and prints "rows cols", so
	// it reports what the second program was actually given.
	cmds, err := tape.Parse(strings.NewReader(
		"Set Size 40 10\nSpawn " + echoBin + "\nWait /ECHOTUI/ @5s\n" +
			"Resize 72 20\nSpawn stty size\nWait /20 72/ @5s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tape.NewPlayer().Run(cmds); err != nil {
		t.Fatalf("the program spawned after a Resize did not see the new size: %v", err)
	}
}
