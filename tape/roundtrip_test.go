package tape_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// scriptReader plays pre-baked input chunks with a pause before each, standing
// in for a person typing at a terminal. The pause has to exceed the session's
// quiet window so that each burst produces its own settle point.
type scriptReader struct {
	chunks []string
	delay  time.Duration
	i      int
}

func (r *scriptReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

// recordFixture records a scripted session against the echotui fixture and
// returns the tape source. It keeps the timing tight so the suite stays fast.
func recordFixture(t *testing.T, rec *tape.Recorder, chunks ...string) string {
	t.Helper()

	sess := &tape.Session{
		Argv:      []string{echoBin},
		In:        &scriptReader{chunks: chunks, delay: 150 * time.Millisecond},
		Out:       io.Discard,
		Cols:      40,
		Rows:      10,
		Quiet:     50 * time.Millisecond,
		SettleMax: 2 * time.Second,
		Recorder:  rec,
	}

	cmds, err := sess.Run()
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	return strings.TrimRight(tape.Sprint(cmds), "\n")
}

// TestRecordReplayRoundTrip is the end-to-end claim of this feature: a session
// recorded against a real program must replay faithfully and pass its own
// assertions. It records typing "hello" and then "quit" into the echotui
// fixture, writes the tape out, reads it back, and plays it. If recording
// captured the wrong keys, ordered them wrongly, or generated a wait that never
// comes true, the replay fails here.
func TestRecordReplayRoundTrip(t *testing.T) {
	source := recordFixture(t, tape.NewRecorder(), "hello", "\r", "quit", "\r")
	t.Logf("recorded tape:\n%s", source)

	// The recording must drive the program, not just sit there.
	for _, want := range []string{"Type hello", "Key Enter", "Type quit", "ExpectExit 0"} {
		if !strings.Contains(source, want) {
			t.Errorf("recorded tape is missing %q:\n%s", want, source)
		}
	}

	// The timing policy must have produced real synchronization, not sleeps.
	if !strings.Contains(source, "Wait /") {
		t.Errorf("recording produced no Wait on screen text:\n%s", source)
	}
	if strings.Contains(source, "Sleep") {
		t.Errorf("recording fell back to Sleep when a wait was available:\n%s", source)
	}

	// Now the round trip: parse what was written and play it.
	cmds, err := tape.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatalf("recorded tape does not parse: %v\n%s", err, source)
	}
	if err := tape.NewPlayer().Run(cmds); err != nil {
		t.Fatalf("recorded tape did not replay: %v\n%s", err, source)
	}
}

// TestRecordReplayRoundTripWithSnapshots checks the other half of the promise:
// recording with snapshots on produces goldens that the replay agrees with, so
// the recording doubles as golden generation with no separate -update pass.
func TestRecordReplayRoundTripWithSnapshots(t *testing.T) {
	rec := tape.NewRecorder()
	rec.CaptureSnapshots = true
	source := recordFixture(t, rec, "hello", "\r")

	if !strings.Contains(source, "Snapshot step-01") {
		t.Fatalf("no snapshots in the recording:\n%s", source)
	}

	goldenDir := t.TempDir()
	files := rec.SnapshotFiles()
	if len(files) == 0 {
		t.Fatal("recorder captured no snapshot contents")
	}
	for name, content := range files {
		p := filepath.Join(goldenDir, name+".golden")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cmds, err := tape.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := tape.NewPlayer()
	p.GoldenDir = goldenDir
	// Update stays off: the goldens must already agree with what replay sees.
	if err := p.Run(cmds); err != nil {
		t.Fatalf("snapshots captured while recording do not match on replay: %v\n%s", err, source)
	}
}

// TestRecordCapturesExitCode checks that a program exiting non-zero during a
// recording is asserted on, so the tape notices if that stops happening.
func TestRecordCapturesExitCode(t *testing.T) {
	source := recordFixture(t, tape.NewRecorder(), "boom", "\r")

	if !strings.Contains(source, "ExpectExit 3") {
		t.Fatalf("expected ExpectExit 3 for the fixture's boom path:\n%s", source)
	}

	cmds, err := tape.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := tape.NewPlayer().Run(cmds); err != nil {
		t.Fatalf("replay: %v\n%s", err, source)
	}
}

// TestRecordStopKey checks that the stop key ends the recording and is not
// passed through to the program under test.
func TestRecordStopKey(t *testing.T) {
	rec := tape.NewRecorder()
	sess := &tape.Session{
		Argv:      []string{echoBin},
		In:        &scriptReader{chunks: []string{"ab\x1dcd"}, delay: 100 * time.Millisecond},
		Out:       io.Discard,
		Cols:      40,
		Rows:      10,
		Quiet:     50 * time.Millisecond,
		SettleMax: time.Second,
		StopKey:   0x1d,
		Recorder:  rec,
	}
	cmds, err := sess.Run()
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	source := tape.Sprint(cmds)
	if !strings.Contains(source, "Type ab") {
		t.Errorf("input before the stop key was lost:\n%s", source)
	}
	if strings.Contains(source, "cd") {
		t.Errorf("input after the stop key was recorded:\n%s", source)
	}
	if strings.Contains(source, "Ctrl+]") {
		t.Errorf("the stop key itself leaked into the tape:\n%s", source)
	}
}
