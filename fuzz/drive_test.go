package fuzz

import (
	"context"
	"runtime"
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
	opts := Options{
		Argv:          argv,
		SettleTimeout: 5 * time.Second,
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
