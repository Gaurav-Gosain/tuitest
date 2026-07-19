package tape_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

func mustParse(t *testing.T, src string) []tape.Command {
	t.Helper()
	cmds, err := tape.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cmds
}

// TestReplayRendersProgramOutput checks the point of replay: the program under
// test is actually drawn to the operator's terminal while the tape drives it.
func TestReplayRendersProgramOutput(t *testing.T) {
	var render bytes.Buffer
	r := &tape.Replayer{Render: &render, Log: io.Discard}

	err := r.Run(mustParse(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /ECHOTUI/ +Screen @5s\n"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !strings.Contains(render.String(), "ECHOTUI") {
		t.Errorf("program output was not rendered; got %q", render.String())
	}
}

// TestReplayEchoesCommands checks the command trace an operator watches.
func TestReplayEchoesCommands(t *testing.T) {
	var log bytes.Buffer
	r := &tape.Replayer{Render: io.Discard, Log: &log, Echo: true}

	if err := r.Run(mustParse(t, "Sleep 1ms\nHide\nShow\n")); err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, want := range []string{"Sleep 1ms", "Hide", "Show"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("command %q was not echoed; log was %q", want, log.String())
		}
	}
}

// TestReplaySpeedScalesSleep checks that -speed actually shortens playback. The
// tape sleeps for 400ms; at 8x it must finish far sooner, and the assertion is
// deliberately loose so a slow machine does not make it flaky.
func TestReplaySpeedScalesSleep(t *testing.T) {
	cmds := mustParse(t, "Sleep 400ms\n")

	start := time.Now()
	r := &tape.Replayer{Render: io.Discard, Log: io.Discard, Speed: 8}
	if err := r.Run(cmds); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("Sleep 400ms at 8x speed took %v, so the speed control did nothing", elapsed)
	}
}

// TestReplayStepModePausesPerCommand checks that step mode consumes exactly one
// line of operator input per command, which is what makes it a stepper rather
// than a pause at the start.
func TestReplayStepModePausesPerCommand(t *testing.T) {
	steps := &countingReader{r: strings.NewReader("\n\n\n")}
	r := &tape.Replayer{
		Render: io.Discard, Log: io.Discard,
		Step: true, StepIn: steps,
	}
	if err := r.Run(mustParse(t, "Sleep 1ms\nHide\nShow\n")); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if steps.lines != 3 {
		t.Errorf("step mode consumed %d lines for 3 commands, want 3", steps.lines)
	}
}

// TestReplayStepModeQuitAborts checks that 'q' stops a stepped replay and that
// the remaining commands do not run.
func TestReplayStepModeQuitAborts(t *testing.T) {
	var log bytes.Buffer
	r := &tape.Replayer{
		Render: io.Discard, Log: &log, Echo: true,
		Step: true, StepIn: strings.NewReader("\nq\n"),
	}
	err := r.Run(mustParse(t, "Sleep 1ms\nHide\nShow\n"))
	if !errors.Is(err, tape.ErrStepAborted) {
		t.Fatalf("expected ErrStepAborted, got %v", err)
	}
	if strings.Contains(log.String(), "Show") {
		t.Errorf("commands after the abort still ran; log was %q", log.String())
	}
}

// TestReplayShowsSnapshotDiffOnFailure is the debugging payoff: when an
// assertion fails, the operator must be shown the expected and actual screens,
// not just told that something did not match.
func TestReplayShowsSnapshotDiffOnFailure(t *testing.T) {
	goldenDir := t.TempDir()
	golden := filepath.Join(goldenDir, "banner.golden")
	if err := os.WriteFile(golden, []byte("TOTALLY WRONG SCREEN"), 0o600); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	r := &tape.Replayer{
		Render: io.Discard, Log: &log,
		GoldenDir: goldenDir, Width: 30,
	}
	src := "Set Size 20 4\nSpawn " + echoBin + "\nWait /ECHOTUI/ +Screen @5s\nWaitStable @2s\nSnapshot banner\n"

	err := r.Run(mustParse(t, src))
	if err == nil {
		t.Fatal("replay of a mismatched snapshot succeeded, so nothing was asserted")
	}

	out := log.String()
	for _, want := range []string{"FAIL snapshot", "expected (golden)", "actual (screen)", "TOTALLY WRONG SCREEN", "ECHOTUI"} {
		if !strings.Contains(out, want) {
			t.Errorf("failure report is missing %q; report was:\n%s", want, out)
		}
	}

	// The typed error must survive so callers can render it themselves.
	var snapErr *tape.SnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected a *tape.SnapshotError, got %T", err)
	}
	if snapErr.Want != "TOTALLY WRONG SCREEN" {
		t.Errorf("SnapshotError.Want = %q, want the golden contents", snapErr.Want)
	}
	if !strings.Contains(snapErr.Got, "ECHOTUI") {
		t.Errorf("SnapshotError.Got = %q, want the actual screen", snapErr.Got)
	}
}

// TestReplayShowsExpectFailure covers the other assertion type, where there is a
// pattern rather than a second screen to compare against.
func TestReplayShowsExpectFailure(t *testing.T) {
	var log bytes.Buffer
	r := &tape.Replayer{Render: io.Discard, Log: &log}
	src := "Set Size 20 4\nSpawn " + echoBin + "\nWait /ECHOTUI/ +Screen @5s\nExpect /NOTHING LIKE THIS/ +Screen\n"

	err := r.Run(mustParse(t, src))
	if err == nil {
		t.Fatal("replay of a failing Expect succeeded")
	}
	out := log.String()
	if !strings.Contains(out, "FAIL expect") || !strings.Contains(out, "ECHOTUI") {
		t.Errorf("expect failure report is missing the pattern or the screen:\n%s", out)
	}

	var expErr *tape.ExpectError
	if !errors.As(err, &expErr) {
		t.Fatalf("expected a *tape.ExpectError, got %T", err)
	}
}

// TestSideBySide checks the column rendering itself, including the gutter that
// marks differing rows and the clipping of overlong lines.
func TestSideBySide(t *testing.T) {
	got := tape.SideBySide("want", "same\nleft", "got", "same\nright", 8)

	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected a header, a rule, and two rows; got %d lines:\n%s", len(lines), got)
	}
	if strings.Contains(lines[2], "|") {
		t.Errorf("identical rows must not be marked as differing: %q", lines[2])
	}
	if !strings.Contains(lines[3], "|") {
		t.Errorf("differing rows must be marked: %q", lines[3])
	}

	// A row present on only one side still counts as a difference.
	got = tape.SideBySide("want", "a", "got", "a\nextra", 8)
	if !strings.Contains(strings.Split(got, "\n")[3], "|") {
		t.Errorf("a row missing from one side must be marked:\n%s", got)
	}

	// Overlong content is clipped to the column width.
	got = tape.SideBySide("want", strings.Repeat("x", 40), "got", "y", 8)
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 8*2+3 {
			t.Errorf("line exceeds the column budget: %q", line)
		}
	}
}

// countingReader counts how many lines were read through it.
type countingReader struct {
	r     io.Reader
	lines int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.lines += bytes.Count(p[:n], []byte("\n"))
	return n, err
}
