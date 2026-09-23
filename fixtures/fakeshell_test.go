package fixtures

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestFakeShellDeliversOutputOnce pins that queued output is read exactly once.
// SendOutput used to put the data in the buffer and on a channel as well, so
// the second Read returned the same bytes again.
func TestFakeShellDeliversOutputOnce(t *testing.T) {
	f := NewFakeShell()
	f.SendOutput("hello")

	buf := make([]byte, 64)
	n, err := f.Read(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("first read = %q, %v; want \"hello\"", buf[:n], err)
	}
	n, err = f.ReadWithTimeout(buf, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("second read returned %q; the output was already consumed", buf[:n])
	}
}

// TestFakeShellShortReadsKeepTheRest pins that a read into a small buffer
// leaves the remainder for the next read rather than dropping it.
func TestFakeShellShortReadsKeepTheRest(t *testing.T) {
	f := NewFakeShell()
	done := make(chan string)
	go func() {
		var got strings.Builder
		buf := make([]byte, 2)
		for got.Len() < len("hello world") {
			n, err := f.ReadWithTimeout(buf, time.Second)
			if err != nil {
				break
			}
			got.Write(buf[:n])
		}
		done <- got.String()
	}()

	f.SendOutput("hello")
	f.SendOutput(" world")

	if got := <-done; got != "hello world" {
		t.Fatalf("read %q, want \"hello world\"", got)
	}
}

// TestFakeShellBlockedReadWakesOnOutputAndClose pins that a Read waiting for
// output returns when output arrives, and returns io.EOF when the shell closes.
func TestFakeShellBlockedReadWakesOnOutputAndClose(t *testing.T) {
	f := NewFakeShell()
	type result struct {
		s   string
		err error
	}
	read := func() <-chan result {
		ch := make(chan result, 1)
		go func() {
			buf := make([]byte, 16)
			n, err := f.Read(buf)
			ch <- result{string(buf[:n]), err}
		}()
		return ch
	}

	pending := read()
	time.Sleep(10 * time.Millisecond)
	f.SendOutput("x")
	select {
	case r := <-pending:
		if r.err != nil || r.s != "x" {
			t.Fatalf("read = %q, %v; want \"x\"", r.s, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("a blocked Read did not wake when output arrived")
	}

	pending = read()
	time.Sleep(10 * time.Millisecond)
	_ = f.Close()
	select {
	case r := <-pending:
		if r.err != io.EOF {
			t.Fatalf("read after close = %q, %v; want io.EOF", r.s, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("a blocked Read did not wake when the shell closed")
	}
}

// TestFakeShellRecordsInput checks the input side: Write records what the
// terminal sent, and ClearInput forgets it.
func TestFakeShellRecordsInput(t *testing.T) {
	f := NewFakeShell()
	_, _ = f.Write([]byte("ls"))
	_, _ = f.Write([]byte("\r"))
	if got := f.GetInput(); got != "ls\r" {
		t.Fatalf("GetInput = %q", got)
	}
	if h := f.GetInputHistory(); len(h) != 2 || h[0] != "ls" || h[1] != "\r" {
		t.Fatalf("GetInputHistory = %q", h)
	}
	f.ClearInput()
	if f.GetInput() != "" || len(f.GetInputHistory()) != 0 {
		t.Fatal("ClearInput left input behind")
	}
	_ = f.Close()
	if _, err := f.Write([]byte("x")); err != io.EOF {
		t.Fatalf("Write after Close = %v, want io.EOF", err)
	}
}

// TestProgressBarStaysInsideItsWidth pins that an out-of-range percentage is
// clamped rather than drawing a bar longer than width.
func TestProgressBarStaysInsideItsWidth(t *testing.T) {
	for _, pct := range []int{-50, 0, 50, 100, 150} {
		bar := ProgressBar(pct, 10)
		inner := bar[strings.Index(bar, "[")+1 : strings.Index(bar, "]")]
		if len(inner) != 10 {
			t.Errorf("ProgressBar(%d, 10) inner = %q, want 10 cells", pct, inner)
		}
	}
}

func TestANSIBuilder(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"cursor", NewANSIBuilder().CursorTo(2, 3).CursorUp(1).CursorDown(4).String(), "\x1b[2;3H\x1b[A\x1b[4B"},
		{"sgr", NewANSIBuilder().Bold().FgRGB(1, 2, 3).Reset().String(), "\x1b[1m\x1b[38;2;1;2;3m\x1b[0m"},
		{"empty sgr", NewANSIBuilder().SGR().String(), "\x1b[m"},
		{"region", NewANSIBuilder().ScrollRegion(1, 5).ScrollUp(2).String(), "\x1b[1;5r\x1b[2S"},
		{"clear", NewANSIBuilder().Text("x").Clear().Text("y").String(), "y"},
		{"hyperlink", NewANSIBuilder().OSCHyperlink("u", "t").String(), "\x1b]8;;u\x1b\\t\x1b]8;;\x1b\\"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Fatalf("got %q, want %q", c.got, c.want)
			}
		})
	}
}
