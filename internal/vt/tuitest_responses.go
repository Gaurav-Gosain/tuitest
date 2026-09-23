package vt

// This file belongs to tuitest, not to the upstream copy: scripts/vendor-vt.sh
// never touches a file named tuitest_*. tuios reads query responses with a
// goroutine blocked in Read. tuitest drains them after each Write from the
// output loop instead, so it needs a read that never blocks.

// takeAll removes and returns everything currently buffered without blocking.
// It returns nil when the buffer is empty, so a caller draining after every
// write does not allocate on the common path where nothing was produced.
func (p *bufPipe) takeAll() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buf.Len() == 0 {
		return nil
	}
	out := make([]byte, p.buf.Len())
	_, _ = p.buf.Read(out)
	return out
}

// TakeResponses removes and returns any bytes the emulator has queued to send
// back to the program, such as cursor position reports, colour queries, and
// device attributes. Unlike Read it never blocks, so a caller can drain after
// each Write without a dedicated goroutine. It returns nil when there is
// nothing queued.
func (e *Emulator) TakeResponses() []byte {
	if e.closed.Load() {
		return nil
	}
	return e.pipe.takeAll()
}
