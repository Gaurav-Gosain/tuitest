package tuitest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// failingTB stands in for a test that has failed, so StartT's cleanup can be
// run and checked without failing the real test.
type failingTB struct {
	testing.TB
	failed bool

	mu       sync.Mutex
	logs     []string
	cleanups []func()
}

func (f *failingTB) Failed() bool { return f.failed }
func (f *failingTB) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}
func (f *failingTB) Errorf(format string, args ...any) { f.Logf(format, args...) }
func (f *failingTB) Cleanup(fn func())                 { f.cleanups = append(f.cleanups, fn) }
func (f *failingTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

// castEvent is one parsed event line.
type castEvent struct {
	t    float64
	kind string
	data string
}

// readCast parses an asciicast v2 file into its header and events.
func readCast(t *testing.T, path string) (map[string]any, []castEvent) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	if !sc.Scan() {
		t.Fatal("the recording is empty")
	}
	var header map[string]any
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		t.Fatalf("header: %v", err)
	}
	var events []castEvent
	last := 0.0
	for sc.Scan() {
		var raw []any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil || len(raw) != 3 {
			t.Fatalf("event line %q: %v", sc.Text(), err)
		}
		ev := castEvent{t: raw[0].(float64), kind: raw[1].(string), data: raw[2].(string)}
		if ev.t < last {
			t.Fatalf("event at %f comes after one at %f", ev.t, last)
		}
		last = ev.t
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return header, events
}

// A failed test leaves a recording a player can scrub: the output, the input,
// a resize, and a mark for each wait and input call, in order.
func TestFailedTestWritesCast(t *testing.T) {
	dir := t.TempDir()
	tb := &failingTB{TB: t, failed: true}
	term := StartT(tb, []string{"/bin/sh", "-c", "stty -echo; echo ready; read x; echo \"got $x\"; exec sleep 30"},
		WithCastDir(dir), WithKillGrace(-1))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.SendKeys("hello", Enter); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("got hello", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	_ = term.WaitForText("never shown", 50*time.Millisecond)
	tb.runCleanups()

	files, _ := filepath.Glob(filepath.Join(dir, "*.cast"))
	if len(files) != 1 {
		t.Fatalf("found %d recordings in %s, want 1; log: %q", len(files), dir, tb.logs)
	}
	if !strings.Contains(strings.Join(tb.logs, "\n"), files[0]) {
		t.Fatalf("the test log does not name the recording %s: %q", files[0], tb.logs)
	}
	header, events := readCast(t, files[0])
	if header["version"] != float64(2) || header["width"] != float64(80) || header["height"] != float64(24) {
		t.Fatalf("header = %v, want version 2 and 80x24", header)
	}
	want := []castEvent{
		{kind: "m", data: `WaitForText: text "ready"`},
		{kind: "o", data: "ready"},
		{kind: "m", data: "WaitForText: done"},
		{kind: "m", data: "SendKeys"},
		{kind: "i", data: "hello\r"},
		{kind: "m", data: `WaitForText: text "got hello"`},
		{kind: "o", data: "got hello"},
		{kind: "m", data: "WaitForText: done"},
		{kind: "m", data: "Resize 100x30"},
		{kind: "r", data: "100x30"},
		{kind: "m", data: `WaitForText: text "never shown"`},
		{kind: "m", data: "WaitForText: timed out"},
	}
	i := 0
	for _, ev := range events {
		if i < len(want) && ev.kind == want[i].kind && strings.Contains(ev.data, want[i].data) {
			i++
		}
	}
	if i < len(want) {
		t.Fatalf("the recording lacks %q (%s), or has it out of order; events:\n%v", want[i].data, want[i].kind, events)
	}
}

// Playing the output events back through an emulator gives the screen the
// test saw, so the recording shows the failure as it was.
func TestCastReplaysToTheSameScreen(t *testing.T) {
	dir := t.TempDir()
	tb := &failingTB{TB: t, failed: true}
	term := StartT(tb, []string{"/bin/sh", "-c", "printf '\\033[2J\\033[5;10Hboxed \\033[1mbold\\033[0m\\r\\nnext line\\n'; exec sleep 30"},
		WithCastDir(dir), WithKillGrace(-1))
	if err := term.WaitForText("next line", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(60, 20); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForDesc("the resize", func(s Screen) bool { c, _ := s.Size(); return c == 60 }, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	seen := term.Screen().Text()
	tb.runCleanups()

	files, _ := filepath.Glob(filepath.Join(dir, "*.cast"))
	if len(files) != 1 {
		t.Fatalf("found %v, want one recording", files)
	}
	_, events := readCast(t, files[0])
	e := emu.New(80, 24)
	for _, ev := range events {
		switch ev.kind {
		case "o":
			_, _ = e.Write([]byte(ev.data))
		case "r":
			var c, r int
			fmt.Sscanf(ev.data, "%dx%d", &c, &r)
			e.Resize(c, r)
		}
	}
	if got := snapshotOf(e, 60, 20).Text(); got != seen {
		t.Fatalf("the replayed screen differs from the one the test saw:\n--- replayed ---\n%s\n--- seen ---\n%s", got, seen)
	}
}

// A test that passes writes nothing.
func TestPassedTestWritesNoCast(t *testing.T) {
	dir := t.TempDir()
	tb := &failingTB{TB: t}
	term := StartT(tb, []string{"/bin/sh", "-c", "echo ready"}, WithCastDir(dir))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	tb.runCleanups()
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Fatalf("a passing test wrote %v", files)
	}
}

// TUITEST_CAST_DIR turns recording on for StartProgramT as well.
func TestCastDirFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(CastDirEnv, dir)
	tb := &failingTB{TB: t, failed: true}
	term := StartProgramT(tb, newCatProgram(""))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	tb.runCleanups()
	files, _ := filepath.Glob(filepath.Join(dir, "TestCastDirFromEnvironment-*.cast"))
	if len(files) != 1 {
		t.Fatalf("found %v, want one recording named after the test", files)
	}
}

func TestWriteCastWithoutRecording(t *testing.T) {
	term := StartT(t, []string{"/bin/sh", "-c", "echo ready"})
	if err := term.WriteCast(&strings.Builder{}); err == nil {
		t.Fatal("WriteCast returned no error for a terminal that recorded nothing")
	}
}

// A rune split across two chunks of output is one event: asciicast carries
// text, and half a rune is not text.
func TestCastKeepsSplitRunesWhole(t *testing.T) {
	r := newCastRecorder(80, 24, "xterm")
	r.output([]byte("caf\xc3"))
	r.output([]byte("\xa9!"))
	var b strings.Builder
	if err := r.writeTo(&b, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"o","caf"`) || !strings.Contains(b.String(), `"o","é!"`) {
		t.Fatalf("the split rune was not kept whole:\n%s", b.String())
	}
}

// A recording stops at its limit with a mark, so a flood cannot use up the
// test's memory.
func TestCastStopsAtLimit(t *testing.T) {
	r := newCastRecorder(80, 24, "xterm")
	r.limit = 200
	for range 50 {
		r.output([]byte("0123456789"))
	}
	var b strings.Builder
	if err := r.writeTo(&b, ""); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 400 || !strings.Contains(b.String(), "recording stopped") {
		t.Fatalf("the recording did not stop at its limit (%d bytes):\n%s", b.Len(), b.String())
	}
}
