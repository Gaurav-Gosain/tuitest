package tape_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

var echoBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tuitest-tape-fixture-")
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
