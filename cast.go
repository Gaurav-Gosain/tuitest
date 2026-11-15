package tuitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// CastDirEnv names the environment variable that turns on recording for
// StartT and StartProgramT. When it is set, a terminal whose test fails is
// written to that directory as an asciicast v2 file. See WithCastDir.
const CastDirEnv = "TUITEST_CAST_DIR"

// castLimit bounds how many bytes of events one recording keeps. A program
// that floods output past it is recorded up to that point, and a mark says
// where the recording stopped.
const castLimit = 16 << 20

// WithCastDir records everything the terminal shows and is sent, as an
// asciicast v2 recording that asciinema and other players can scrub. Every
// wait and every input call adds a mark, so a player can jump to each step of
// the test.
//
// Under StartT and StartProgramT a terminal whose test fails is written to a
// new file in dir, and the test log names the file. A test that passes writes
// nothing. Setting TUITEST_CAST_DIR does the same for every terminal those
// functions start. WriteCast writes the recording at any time.
//
// Recording keeps the output in memory, up to 16 MiB for each terminal, so it
// is off unless one of the two asks for it.
func WithCastDir(dir string) Option {
	return func(c *config) { c.castDir = dir }
}

// castRecorder keeps the events of one terminal in asciicast v2 form, one JSON
// array per line.
type castRecorder struct {
	mu        sync.Mutex
	start     time.Time
	cols      int
	rows      int
	term      string
	events    bytes.Buffer
	stopped   bool
	carry     map[string][]byte // an unfinished UTF-8 rune at the end of a stream
	limit     int
	lastEvent float64
}

func newCastRecorder(cols, rows int, term string) *castRecorder {
	return &castRecorder{start: time.Now(), cols: cols, rows: rows, term: term, carry: map[string][]byte{}, limit: castLimit}
}

// event appends one event. kind is "o" (output), "i" (input), "r" (resize)
// or "m" (mark).
func (r *castRecorder) event(kind, data string) {
	if r.stopped {
		return
	}
	t := time.Since(r.start).Seconds()
	// Events must not go back in time, which a clock step could make them do.
	t = max(t, r.lastEvent)
	r.lastEvent = t
	line, _ := json.Marshal([]any{json.Number(strconv.FormatFloat(t, 'f', 6, 64)), kind, data})
	if r.events.Len()+len(line) > r.limit {
		r.stopped = true
		line, _ = json.Marshal([]any{json.Number(strconv.FormatFloat(t, 'f', 6, 64)), "m", fmt.Sprintf("recording stopped: it reached the limit of %d bytes", r.limit)})
	}
	r.events.Write(line)
	r.events.WriteByte('\n')
}

// stream records bytes of output or input. asciicast carries text, so a rune
// split across two chunks is held back until the rest of it arrives.
func (r *castRecorder) stream(kind string, p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := append(r.carry[kind], p...)
	cut := len(b)
	// Hold back at most the last three bytes, the longest unfinished rune.
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				cut = i
			}
			break
		}
	}
	r.carry[kind] = append([]byte(nil), b[cut:]...)
	if cut > 0 {
		r.event(kind, string(b[:cut]))
	}
}

func (r *castRecorder) output(p []byte) { r.stream("o", p) }

func (r *castRecorder) input(op string, p []byte) {
	r.mu.Lock()
	r.event("m", op)
	r.mu.Unlock()
	r.stream("i", p)
}

func (r *castRecorder) resize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.event("m", fmt.Sprintf("Resize %dx%d", cols, rows))
	r.event("r", fmt.Sprintf("%dx%d", cols, rows))
}

func (r *castRecorder) mark(label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.event("m", label)
}

// writeTo writes the header and the events.
func (r *castRecorder) writeTo(w io.Writer, title string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	header := map[string]any{
		"version":   2,
		"width":     r.cols,
		"height":    r.rows,
		"timestamp": r.start.Unix(),
		"env":       map[string]string{"TERM": r.term},
	}
	if title != "" {
		header["title"] = title
	}
	h, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(h, '\n')); err != nil {
		return err
	}
	_, err = w.Write(r.events.Bytes())
	return err
}

// WriteCast writes the terminal's recording to w as an asciicast v2 file. It
// returns an error when the terminal was started without WithCastDir and
// without TUITEST_CAST_DIR, since then nothing was recorded.
func (t *Terminal) WriteCast(w io.Writer) error {
	if t.cast == nil {
		return fmt.Errorf("tuitest: WriteCast: nothing was recorded; start the terminal with WithCastDir or set %s", CastDirEnv)
	}
	return t.cast.writeTo(w, "")
}

// writeFailureCast writes the recording of a failed test to a new file in the
// cast directory and logs where it is. Caller is the test cleanup.
func (t *Terminal) writeFailureCast(tb testing.TB) {
	if t.cast == nil || t.cfg.castDir == "" {
		return
	}
	if err := os.MkdirAll(t.cfg.castDir, 0o755); err != nil {
		tb.Logf("tuitest: cannot write the recording: %v", err)
		return
	}
	f, err := os.CreateTemp(t.cfg.castDir, castFileName(tb.Name())+"-*.cast")
	if err != nil {
		tb.Logf("tuitest: cannot write the recording: %v", err)
		return
	}
	werr := t.cast.writeTo(f, tb.Name())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		tb.Logf("tuitest: cannot write the recording %s: %v", f.Name(), werr)
		return
	}
	tb.Logf("tuitest: the recording of this terminal is in %s. Play it with asciinema play.", f.Name())
}

// castFileName turns a test name into a file name: subtests become part of
// the name, and anything but letters, digits, '.', '_' and '-' becomes '_'.
func castFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return filepath.Base(b.String())
}
