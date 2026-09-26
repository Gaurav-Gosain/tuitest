package fuzz

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// A program that exits cleanly must not be reported as a crash because a write
// raced its exit. Once the child has gone, a write to the PTY master fails (EIO
// on macOS), but the child is only marked exited after the output pump has
// drained its last output and reaped it. An earlier version classified the
// write error on the spot, saw a child not yet marked exited, and reported
// "driving the program failed: write /dev/ptmx: input/output error". That was
// the false positive behind TestOraclesStaySilentOnAWellBehavedProgram failing
// on macOS: the fixture quits on Ctrl+c, and a later write raced the reaper.
//
// The race is made wide here rather than left to chance. The child fills a
// 1000 by 1000 screen with DECALN hundreds of times and exits, so the pump is
// still emulating its final output, for a second or more, after the child is
// gone. Writes sent in that window fail while the child is not yet reaped.
func TestWriteRacingACleanExitIsNotACrash(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}

	const script = `i=0; while [ $i -lt 400 ]; do printf '\033#8'; i=$((i+1)); done; exit 0`
	argv := []string{"/bin/sh", "-c", script}
	// SettleTimeout bounds how long a failed write waits for the reap, and the
	// pump spends a second or more emulating the script's output on an idle
	// machine, several times that on a loaded one. Past the bound the write
	// error is classified as it stands, which is the false crash this test
	// exists to rule out, so the bound is set far above any plausible drain
	// time. It costs nothing: the wait returns as soon as the child is reaped.
	opts := Options{
		Argv:          argv,
		SettleTimeout: time.Minute,
		Limits:        DefaultLimits(),
		Gen:           Config{Cols: 1000, Rows: 1000},
	}.withDefaults()
	// The script never restores anything because it never changed anything,
	// but the point of the test is the crash classification alone.
	opts.Limits.AllowDirtyExit = true

	cmds := []tape.Command{
		{Kind: tape.KindSet, SetKey: "Size", SetArgs: []string{"1000", "1000"}},
		{Kind: tape.KindSpawn, Argv: argv},
		{Kind: tape.KindSleep, Dur: 300 * time.Millisecond},
	}
	for range 20 {
		cmds = append(cmds,
			tape.Command{Kind: tape.KindRaw, Text: "x"},
			tape.Command{Kind: tape.KindSleep, Dur: 50 * time.Millisecond},
		)
	}

	f, err := driveReportingSpawn(context.Background(), opts, cmds)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if f != nil {
		t.Fatalf("a program that exited 0 was reported as %s: %s", f.Kind, f.Detail)
	}
}

// The screen-model check compares the grid against the size the program was
// spawned at, which is whatever the tape's Set Size said, not the size the
// session's generator is configured for. The two differ whenever a corpus entry
// written by a session with --cols 100 is replayed by one with the default 80,
// and whenever the shrinker tries a candidate without the Set line. An earlier
// version took the size from the session's options and reported both as
// "grid is 100x30 but the last requested size was 80x24", a finding about the
// harness that pinned a regression on the program.
func TestScreenModelIsJudgedAgainstTheSpawnedSize(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}

	argv := []string{"/bin/sh", "-c", "printf ready; sleep 5"}
	opts := Options{
		Argv:          argv,
		SettleTimeout: 500 * time.Millisecond,
		Limits:        DefaultLimits(),
		Gen:           Config{Cols: 80, Rows: 24},
	}.withDefaults()

	cmds := []tape.Command{
		{Kind: tape.KindSet, SetKey: "Size", SetArgs: []string{"100", "30"}},
		{Kind: tape.KindSpawn, Argv: argv},
		{Kind: tape.KindWaitOutput},
		{Kind: tape.KindResize, Cols: 90, Rows: 20},
	}
	f, err := driveReportingSpawn(context.Background(), opts, cmds)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if f != nil && f.Kind == FailScreenInconsistent {
		t.Fatalf("a tape that spawned at 100x30 was judged against the session's 80x24: %s", f.Detail)
	}
}

// cancelOnWrite cancels a context the first time the session logs a finding,
// which is the moment between finding a failure and confirming its reduction.
type cancelOnWrite struct {
	cancel context.CancelFunc
	log    strings.Builder
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "iteration ") {
		w.cancel()
	}
	return w.log.Write(p)
}

// A session interrupted after it found a failure cannot confirm the reduction,
// and it must not say the reduction failed to reproduce. The confirmation replay
// returns nothing on a cancelled context, and the session used to read that as
// "did not reproduce on confirmation; the failure may be timing dependent",
// sending the reader after a flake that was really a Ctrl+C.
func TestInterruptedConfirmationIsNotReportedAsAFlake(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &cancelOnWrite{cancel: cancel}

	res, err := Run(ctx, Options{
		Argv:       []string{"/bin/sh", "-c", "exit 3"},
		Seed:       1,
		Iterations: 1,
		Shrink:     true,
		Out:        out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Failures) != 1 {
		t.Fatalf("want the one crash, got %d findings; log:\n%s", len(res.Failures), out.log.String())
	}
	if res.Failures[0].Verified {
		t.Error("a reduction that was never replayed is marked verified")
	}
	log := out.log.String()
	if strings.Contains(log, "timing dependent") {
		t.Errorf("an interrupted session blamed the program's timing:\n%s", log)
	}
	if !strings.Contains(log, "interrupted") {
		t.Errorf("the log should say the session was interrupted:\n%s", log)
	}
}

// A progress line is due once per interval and not before the first one has
// elapsed, so a short session prints none and a long one prints one every so
// often rather than one per iteration.
func TestProgressIsDueOncePerInterval(t *testing.T) {
	t.Parallel()
	start := time.Unix(1000, 0)
	p := progress{every: 15 * time.Second, last: start}
	steps := []struct {
		at   time.Duration
		want bool
	}{
		{0, false},
		{14 * time.Second, false},
		{15 * time.Second, true},
		{16 * time.Second, false},
		{29 * time.Second, false},
		{31 * time.Second, true},
	}
	for _, s := range steps {
		if got := p.due(start.Add(s.at)); got != s.want {
			t.Fatalf("due at %s = %v, want %v", s.at, got, s.want)
		}
	}
}
