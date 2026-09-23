//go:build !windows

package tape

import (
	"io"
	"syscall"
	"testing"
	"time"
)

// A closed channel is always ready to receive from. The record session selected
// on Resizes without checking whether it was closed, so once a caller closed it
// every settle spun on the zero Size and restarted its quiet window each time
// round. The settle could then only end at SettleMax or on new input, and it
// burned a whole core while it waited.
//
// Verified to fail without the fix: the session uses about a second of CPU in
// the second it waits for input, where the fixed one uses a few milliseconds.
func TestSessionToleratesClosedResizeChannel(t *testing.T) {
	resizes := make(chan Size)
	close(resizes)

	const wait = time.Second
	in, feed := io.Pipe()
	go func() {
		time.Sleep(wait)
		_, _ = feed.Write([]byte{0x1d})
		_ = feed.Close()
	}()

	s := &Session{
		Argv:      []string{"cat"},
		In:        in,
		Out:       io.Discard,
		Resizes:   resizes,
		Quiet:     30 * time.Millisecond,
		SettleMax: 5 * time.Second,
		StopKey:   0x1d,
	}
	before := cpuTime(t)
	if _, err := s.Run(); err != nil {
		t.Fatal(err)
	}
	if used := cpuTime(t) - before; used > wait/3 {
		t.Fatalf("recording used %v of CPU while idle for %v; the settle loop is spinning", used, wait)
	}
}

// cpuTime is the user plus system CPU time this process has used so far.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
