package tape_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// scriptReader plays pre-baked input chunks with a pause before each, standing
// in for a person typing at a terminal. The pause has to exceed the session's
// quiet window so that each burst produces its own settle point. The tests that
// use it assert only on how the input was decoded, which no settle can change.
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

// promptReader plays pre-baked input chunks, standing in for a person typing
// at a terminal who looks at the screen before each key: a chunk is only handed
// over once the program has answered everything before it. echotui answers each
// line with a fresh prompt, so before a chunk the output must hold one prompt
// for the opening screen and one per line already sent.
//
// After the last chunk the reader does not end the stream. It blocks, as a
// person who has typed their last key and is watching the program quit, and the
// session ends because the program exited, not because the input did.
type promptReader struct {
	chunks []string
	out    *outputLog
	i      int
	sent   int // lines sent so far
	hold   <-chan struct{}
}

func (r *promptReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		select {
		case <-r.hold:
		case <-time.After(scriptLimit):
		}
		return 0, io.EOF
	}
	// Returns false only at the limit, when the program has stopped answering;
	// the chunk is sent anyway and the test fails on what the tape shows.
	r.out.waitFor(func(s string) bool { return strings.Count(s, "> ") >= 1+r.sent })
	// A pause between keys, as a person leaves, so the recording has time to
	// see each one on its own. It sets no verdict: the settles below end on the
	// next chunk, not on the clock.
	time.Sleep(20 * time.Millisecond)
	chunk := r.chunks[r.i]
	r.sent += strings.Count(chunk, "\r")
	n := copy(p, chunk)
	r.i++
	return n, nil
}

// outputLog collects what the program wrote, through the session's output
// mirror, so the reader can wait on it. The mirror is fed after the emulator
// has taken the same bytes, so once the log shows a reaction the screen the
// recorder snapshots shows it too.
type outputLog struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  strings.Builder
}

func newOutputLog() *outputLog {
	l := &outputLog{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *outputLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf.Write(p)
	l.cond.Broadcast()
	l.mu.Unlock()
	return len(p), nil
}

// waitFor blocks until cond holds on the output so far, or scriptLimit passes.
func (l *outputLog) waitFor(cond func(string) bool) bool {
	deadline := time.Now().Add(scriptLimit)
	// A timer wakes the wait at the deadline, since a program that has stopped
	// writing never broadcasts again.
	stop := time.AfterFunc(scriptLimit, func() {
		l.mu.Lock()
		l.cond.Broadcast()
		l.mu.Unlock()
	})
	defer stop.Stop()
	l.mu.Lock()
	defer l.mu.Unlock()
	for !cond(l.buf.String()) {
		if !time.Now().Before(deadline) {
			return false
		}
		l.cond.Wait()
	}
	return true
}

// scriptLimit bounds every wait in a scripted recording. It is only reached if
// the program stops answering, and the test then fails on what the tape shows.
const scriptLimit = 30 * time.Second

// recordFixture records a scripted session against the echotui fixture and
// returns the tape source. The script must end with a line that makes echotui
// exit.
//
// Every settle here ends on an event rather than a quiet window. Quiet and
// SettleMax are far longer than any script, so a settle after a chunk ends
// when the next chunk arrives, which the reader holds back until the program
// has answered, or when the program exits. The recording used to run with a
// 50ms quiet window, and on a loaded machine a settle ended before the program
// had answered: the opening snapshot caught the banner without its prompt, and
// a session whose input ended straight after "quit" was recorded before the
// program had been reaped, without its ExpectExit.
func recordFixture(t *testing.T, rec *tape.Recorder, chunks ...string) string {
	t.Helper()

	hold := make(chan struct{})
	defer close(hold)
	out := newOutputLog()
	sess := &tape.Session{
		Argv:      []string{echoBin},
		In:        &promptReader{chunks: chunks, out: out, hold: hold},
		Out:       out,
		Cols:      40,
		Rows:      10,
		Quiet:     scriptLimit,
		SettleMax: scriptLimit,
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
	source := recordFixture(t, rec, "hello", "\r", "quit", "\r")

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
