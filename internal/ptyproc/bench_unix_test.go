//go:build !windows

package ptyproc

import (
	"os"
	"testing"
	"time"
)

// BenchmarkCloseRunningChild measures the teardown every StartT cleanup pays
// for a program still running when the test ends, which is the common case for
// a TUI. It covers finding descendants, signalling, and waiting for the reap.
func BenchmarkCloseRunningChild(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		p, err := Start(Config{Argv: []string{"/bin/sleep", "30"}, Cols: 20, Rows: 5}, Handler{})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := p.Close(); err != nil {
			b.Fatal(err)
		}
		select {
		case <-p.Done():
		case <-time.After(5 * time.Second):
			b.Fatal("child not reaped after Close")
		}
	}
}

// BenchmarkDescendants measures one walk of the process tree from this
// process, the snapshot Close takes before signalling anything.
func BenchmarkDescendants(b *testing.B) {
	self := os.Getpid()
	for b.Loop() {
		_ = descendants(self)
	}
}
