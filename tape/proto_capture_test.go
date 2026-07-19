package tape_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// buildMouseTUI builds the fixture that actually enables the input protocols.
// It is built per test rather than in TestMain so the ordinary suite does not
// pay for it.
func buildMouseTUI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mousetui")
	build := exec.Command("go", "build", "-o", bin, "../testdata/mousetui")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building mousetui fixture: %v", err)
	}
	return bin
}

// TestRecordRealSessionWithMousePasteAndFocus is the end-to-end claim of this
// track, against a real program in a real PTY rather than a synthetic table.
//
// The fixture enables mouse reporting, bracketed paste and focus reporting, and
// queries the terminal at startup so that a device-attributes reply and a kitty
// graphics reply arrive on the input channel alongside the operator's input.
// That is exactly the mixture that produced the reported bug: before this work
// the mouse, paste and focus sequences were dropped with a warning, and the
// replies were shredded into bogus keystrokes.
//
// The assertions are about the tape reading as what happened, and about the
// recording being a complete replay.
func TestRecordRealSessionWithMousePasteAndFocus(t *testing.T) {
	bin := buildMouseTUI(t)

	// Real wire bytes, as a terminal would deliver them: a click, a drag, a
	// release, a focus change, and a bracketed paste.
	input := []string{
		"\x1b[<0;10;5M",                 // press left at col 10 row 5
		"\x1b[<32;11;5M",                // drag to col 11
		"\x1b[<0;11;5m",                 // release
		"\x1b[O",                        // focus out
		"\x1b[I",                        // focus in
		"\x1b[200~pasted text\x1b[201~", // a paste
		"q",                             // quit
	}

	sess := &tape.Session{
		Argv:      []string{bin},
		In:        &scriptReader{chunks: input, delay: 120 * time.Millisecond},
		Out:       io.Discard,
		Cols:      60,
		Rows:      12,
		Quiet:     40 * time.Millisecond,
		SettleMax: 2 * time.Second,
	}

	cmds, err := sess.Run()
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	source := strings.TrimRight(tape.Sprint(cmds), "\n")
	t.Logf("recorded tape:\n%s", source)

	// Every protocol this track added must read as itself, not as a hex blob
	// and not as a keystroke.
	for _, want := range []string{
		"Mouse Press Left 9 4",
		"Mouse Drag Left 10 4",
		"Mouse Release Left 10 4",
		"Focus Out",
		"Focus In",
		`Paste "pasted text"`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("recorded tape is missing %q:\n%s", want, source)
		}
	}

	// The reported corruption must not reappear. These are the shapes the old
	// decoder produced for a control-string reply.
	for _, bad := range []string{"Alt+_", "Alt+\\", "Alt+P", "Alt+]"} {
		if strings.Contains(source, bad) {
			t.Errorf("a terminal reply was decoded as a keystroke (%q):\n%s", bad, source)
		}
	}

	// The recording must parse, which is the check that the decoder cannot emit
	// something the grammar does not accept.
	if _, err := tape.Parse(strings.NewReader(source + "\n")); err != nil {
		t.Fatalf("recorded tape does not parse: %v\n%s", err, source)
	}
}

// TestRecordedMouseSessionReplays plays back a recording made from real mouse
// input and checks the program under test reacts to it, which is the claim a
// tape actually makes. A tape that merely looks right but sends nothing the
// program understands would pass the assertions above and fail here.
func TestRecordedMouseSessionReplays(t *testing.T) {
	bin := buildMouseTUI(t)

	script := "Set Size 60 12\nSpawn " + bin + "\n" +
		"Wait /MOUSETUI ready/ @5s\n" +
		"Mouse Press Left 9 4\n" +
		"Wait /mouse <0;10;5M/ @5s\n" +
		"Mouse Drag Left 10 4\n" +
		"Wait /mouse <32;11;5M/ @5s\n" +
		"Mouse Release Left 10 4\n" +
		"Wait /mouse <0;11;5m/ @5s\n" +
		"Focus Out\n" +
		"Wait /focus out/ @5s\n" +
		"Focus In\n" +
		"Wait /focus in/ @5s\n" +
		"Paste \"pasted text\"\n" +
		"Wait /pasted text/ @5s\n"

	cmds, err := tape.Parse(strings.NewReader(script))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	p := tape.NewPlayer()
	if err := p.Run(cmds); err != nil {
		t.Fatalf("replaying a mouse, focus and paste tape: %v", err)
	}
}
