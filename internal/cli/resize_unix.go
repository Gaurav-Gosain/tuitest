//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/Gaurav-Gosain/tuitest/tape"
	xterm "github.com/charmbracelet/x/term"
)

// watchResize reports terminal size changes until the returned stop function is
// called. The channel is buffered and sends are dropped when full, because a
// resize burst only needs its final size to be recorded.
func watchResize(fd uintptr) (<-chan tape.Size, func()) {
	ch := make(chan tape.Size, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				w, h, err := xterm.GetSize(fd)
				if err != nil {
					continue
				}
				select {
				case ch <- tape.Size{Cols: w, Rows: h}:
				default:
				}
			case <-done:
				return
			}
		}
	}()

	return ch, func() {
		signal.Stop(sig)
		close(done)
	}
}
