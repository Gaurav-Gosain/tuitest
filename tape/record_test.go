package tape

import (
	"strings"
	"testing"
	"time"
)

// recorded renders a recorder's tape without the header, so the assertions read
// as the interesting part of the recording.
func recorded(r *Recorder) string {
	return strings.TrimRight(Encode(r.Commands()), "\n")
}

// TestRecorderCoalescesPerKeystrokeChunks is the regression test for the
// readability bug that matters most: a terminal delivers one read per keystroke,
// so a recorder that finalized its text run on every chunk would emit "Type h",
// "Type e", ... instead of one Type command. Feeding one byte at a time is
// exactly what real typing looks like.
func TestRecorderCoalescesPerKeystrokeChunks(t *testing.T) {
	r := NewRecorder()
	for _, b := range []byte("hello") {
		r.Input([]byte{b})
	}
	r.Input([]byte("\r"))

	got := recorded(r)
	want := "Type hello\nKey Enter"
	if got != want {
		t.Errorf("per-keystroke input did not coalesce:\n got: %q\nwant: %q", got, want)
	}
}

// TestRecorderWaitsOnNewText checks the preferred branch of the timing policy:
// new, distinctive text on screen becomes a Wait on that text.
func TestRecorderWaitsOnNewText(t *testing.T) {
	r := NewRecorder()
	r.Input([]byte("hello\r"))
	r.Settle("ECHOTUI\n>", "ECHOTUI\n> hello\necho: hello\n>", 0)

	got := recorded(r)
	want := "Type hello\nKey Enter\nWait /echo:\\s+hello/ +Screen"
	if got != want {
		t.Errorf("expected a Wait on the new line:\n got: %q\nwant: %q", got, want)
	}
}

// TestRecorderAnchorRegexMatchesTheScreen guards the generated pattern itself:
// it must actually match the screen it was derived from, and must not match the
// screen from before the change (a pattern already on screen would pass
// instantly and synchronize nothing).
func TestRecorderAnchorRegexMatchesTheScreen(t *testing.T) {
	before := "ECHOTUI\n>"
	after := "ECHOTUI\n> hello\necho:   hello\n>"

	r := NewRecorder()
	re, ok := r.anchor(before, after)
	if !ok {
		t.Fatal("no anchor chosen for a screen that clearly changed")
	}
	if !re.MatchString(after) {
		t.Errorf("anchor %q does not match the screen it came from:\n%s", re, after)
	}
	if re.MatchString(before) {
		t.Errorf("anchor %q already matched the previous screen, so it waits for nothing", re)
	}
}

// TestRecorderRejectsAnchorAlreadyOnScreen covers the case the whole-line
// novelty check cannot: a line that is new as a line, but whose text already
// appears inside a longer line on the previous screen. Waiting on it would pass
// the instant the wait began and synchronize nothing, so the recorder must fall
// back to WaitStable instead.
func TestRecorderRejectsAnchorAlreadyOnScreen(t *testing.T) {
	before := "loading data now"
	after := "loading data now\nloading data"

	r := NewRecorder()
	if re, ok := r.anchor(before, after); ok {
		t.Errorf("chose anchor %q, which already matches the previous screen %q", re, before)
	}

	r2 := NewRecorder()
	r2.Settle(before, after, 0)
	if got := recorded(r2); got != "WaitStable" {
		t.Errorf("expected a fallback to WaitStable, got %q", got)
	}
}

// TestRecorderFallsBackToWaitStable covers the middle branch: the screen changed
// but every line was already present, so there is nothing distinctive to wait
// on.
func TestRecorderFallsBackToWaitStable(t *testing.T) {
	r := NewRecorder()
	// The second "echo: hi" line is not new text, only a repeat, so no anchor
	// can distinguish the new screen from the old one.
	r.Settle("echo: hi\n>", "echo: hi\n>\necho: hi", 0)

	if got := recorded(r); got != "WaitStable" {
		t.Errorf("expected WaitStable when no line is new, got %q", got)
	}
}

// TestRecorderEmitsNothingWhenScreenUnchanged checks that a human's think-time
// does not become a tape command.
func TestRecorderEmitsNothingWhenScreenUnchanged(t *testing.T) {
	r := NewRecorder()
	r.Settle("same", "same", 3*time.Second)

	if got := recorded(r); got != "" {
		t.Errorf("idle time became a command: %q", got)
	}
}

// TestRecorderIdleSleepFallback checks the explicit opt-in: with IdleSleep set,
// a long silent pause does become a Sleep.
func TestRecorderIdleSleepFallback(t *testing.T) {
	r := NewRecorder()
	r.IdleSleep = time.Second
	r.Settle("same", "same", 2*time.Second)

	if got := recorded(r); got != "Sleep 2s" {
		t.Errorf("expected the idle pause to become Sleep 2s, got %q", got)
	}

	// Below the threshold it stays silent.
	r2 := NewRecorder()
	r2.IdleSleep = time.Second
	r2.Settle("same", "same", 200*time.Millisecond)
	if got := recorded(r2); got != "" {
		t.Errorf("a short pause should emit nothing, got %q", got)
	}
}

// TestRecorderSnapshotsAtSettlePoints checks that -snapshots emits a Snapshot per
// settle point and retains the screen behind it, which is what lets a recording
// double as golden generation.
func TestRecorderSnapshotsAtSettlePoints(t *testing.T) {
	r := NewRecorder()
	r.CaptureSnapshots = true
	r.Settle("", "first screen", 0)
	r.Settle("first screen", "second screen", 0)

	got := recorded(r)
	want := "Wait /first\\s+screen/ +Screen\nSnapshot step-01\n" +
		"Wait /second\\s+screen/ +Screen\nSnapshot step-02"
	if got != want {
		t.Errorf("snapshot commands:\n got: %q\nwant: %q", got, want)
	}

	files := r.SnapshotFiles()
	if len(files) != 2 {
		t.Fatalf("SnapshotFiles has %d entries, want 2", len(files))
	}
	if files["step-01"] != "first screen" || files["step-02"] != "second screen" {
		t.Errorf("captured screens do not match the settle points: %#v", files)
	}
}

// TestRecorderOrdersInputBeforeWait checks that input pending when a settle
// point arrives is flushed ahead of the wait it produced, not after it. Getting
// this backwards would emit a tape that waits for output before sending the
// input that causes it, deadlocking on replay.
func TestRecorderOrdersInputBeforeWait(t *testing.T) {
	r := NewRecorder()
	r.Input([]byte("go")) // no trailing key, so the Type run is still open
	r.Settle("before", "before\nafter output", 0)

	got := recorded(r)
	want := "Type go\nWait /after\\s+output/ +Screen"
	if got != want {
		t.Errorf("input and wait are out of order:\n got: %q\nwant: %q", got, want)
	}
}

// TestRecorderHeader checks the opening lines of a recording.
func TestRecorderHeader(t *testing.T) {
	r := NewRecorder()
	r.Header(100, 30, "xterm", []string{"FOO=bar"}, []string{"/bin/prog", "-x"})

	got := recorded(r)
	want := "Set Size 100 30\nSet Term xterm\nSet Env FOO=bar\nSpawn /bin/prog -x"
	if got != want {
		t.Errorf("header:\n got: %q\nwant: %q", got, want)
	}
}
