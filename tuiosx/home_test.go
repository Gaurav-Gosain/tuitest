package tuiosx_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tuiosx"
)

// StartTuios gives tuios its own HOME, as it does every XDG directory. tuios
// reads ~/.claude and ~/.codex, so a test that inherits the real HOME reads the
// developer's files. A stand-in binary reports what it sees, so this runs
// without tuios.
func TestStartTuiosIsolatesHome(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tuios")
	script := "#!/bin/sh\necho \"HOME=$HOME\"\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUIOS_BIN", bin)
	realHome := os.Getenv("HOME")

	term := tuiosx.StartTuios(t)
	if err := term.WaitForText("HOME=", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var home string
	for _, line := range strings.Split(term.Snapshot(), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "HOME="); ok {
			home = v
		}
	}
	if home == "" || home == realHome {
		t.Fatalf("tuios sees HOME=%q, the real HOME is %q; want a directory of its own", home, realHome)
	}
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		t.Fatalf("HOME %q is not a directory: %v", home, err)
	}
}
