package tuitest

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// catProgram is cat -v as a Program: it prints every byte it reads with
// control bytes made visible, after an optional setup string such as a
// keyboard mode. 'q' ends it with exit code 7, and 'P' makes it panic.
type catProgram struct {
	setup string

	mu      sync.Mutex
	out     io.Writer
	stopped chan struct{}
	once    sync.Once
}

func newCatProgram(setup string) *catProgram {
	return &catProgram{setup: setup, stopped: make(chan struct{})}
}

func (p *catProgram) Run(in io.Reader, out io.Writer, cols, rows int) int {
	p.mu.Lock()
	p.out = out
	p.mu.Unlock()
	fmt.Fprintf(out, "%sready %dx%d\r\n", p.setup, cols, rows)
	buf := make([]byte, 256)
	for {
		n, err := in.Read(buf)
		for _, b := range buf[:n] {
			switch {
			case b == 'q':
				return 7
			case b == 'P':
				panic("boom")
			case b == 0x7f:
				io.WriteString(out, "^?")
			case b < 0x20:
				io.WriteString(out, "^"+string(rune(b+0x40)))
			default:
				out.Write([]byte{b})
			}
		}
		if err != nil {
			return 0
		}
	}
}

func (p *catProgram) Resize(cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out != nil {
		fmt.Fprintf(p.out, "[size %dx%d]", cols, rows)
	}
}

func (p *catProgram) Stop() { p.once.Do(func() { close(p.stopped) }) }

// backends start the same cat -v behind each transport, so one test body runs
// against both.
var backends = []struct {
	name  string
	start func(t *testing.T, setup string, opts ...Option) *Terminal
}{
	{"pty", func(t *testing.T, setup string, opts ...Option) *Terminal {
		script := "stty raw -echo; printf '" + setup + "ready 80x24\\r\\n'; exec cat -v"
		return StartT(t, []string{"/bin/sh", "-c", script}, append([]Option{WithKillGrace(-1)}, opts...)...)
	}},
	{"program", func(t *testing.T, setup string, opts ...Option) *Terminal {
		return StartProgramT(t, newCatProgram(strings.ReplaceAll(setup, `\033`, "\x1b")), opts...)
	}},
}

// The same test passes on both transports: the screen, the waits and the key
// encoding do not depend on where the program runs.
func TestProgramBehavesLikePTY(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			term := b.start(t, `\033[>1u`) // kitty keyboard flags
			if err := term.WaitForText("ready 80x24", 5*time.Second); err != nil {
				t.Fatal(err)
			}
			if err := term.SendKeys(Ctrl('b'), "x", Enter, "<end>"); err != nil {
				t.Fatal(err)
			}
			if err := term.WaitForText("^[[98;5ux^M<end>", 5*time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The terminal's answers to the program's queries reach a Program too.
func TestProgramGetsQueryAnswers(t *testing.T) {
	term := StartProgramT(t, newCatProgram("\x1b[6n"))
	// The query comes before any text, so the cursor is at row 1, column 1.
	if err := term.WaitForText("^[[1;1R", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestProgramResize(t *testing.T) {
	term := StartProgramT(t, newCatProgram(""))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("[size 100x30]", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if cols, rows := term.Screen().Size(); cols != 100 || rows != 30 {
		t.Fatalf("screen is %dx%d, want 100x30", cols, rows)
	}
}

func TestProgramExit(t *testing.T) {
	term := StartProgramT(t, newCatProgram(""))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("q"); err != nil {
		t.Fatal(err)
	}
	code, err := term.WaitExit(5 * time.Second)
	if err != nil || code != 7 {
		t.Fatalf("WaitExit = %d, %v; want 7, nil", code, err)
	}
	if st, exited := term.ExitStatus(); !exited || st.Code != 7 || st.Signaled {
		t.Fatalf("ExitStatus = %+v, %v; want code 7", st, exited)
	}
	if err := term.Type("more"); !errors.Is(err, ErrChildExited) {
		t.Fatalf("Type after the exit: got %v, want ErrChildExited", err)
	}
	if err := term.WaitForText("never", time.Second); !errors.Is(err, ErrChildExited) {
		t.Fatalf("a wait after the exit: got %v, want ErrChildExited", err)
	}
	if term.Pid() != 0 {
		t.Fatalf("Pid = %d, want 0 for a program in the test process", term.Pid())
	}
}

// A panic in Run ends the program the way it ends a Go program on a PTY: the
// panic shows on the screen and the exit code is 2. It does not end the test
// binary.
func TestProgramPanic(t *testing.T) {
	term := StartProgramT(t, newCatProgram(""))
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("P"); err != nil {
		t.Fatal(err)
	}
	code, err := term.WaitExit(5 * time.Second)
	if err != nil || code != 2 {
		t.Fatalf("WaitExit = %d, %v; want 2, nil", code, err)
	}
	if !strings.Contains(term.Screen().Text(), "panic: boom") {
		t.Fatalf("the screen does not show the panic:\n%s", term.Screen().Text())
	}
}

// Close stops a program that is waiting for input.
func TestProgramClose(t *testing.T) {
	term, err := StartProgram(newCatProgram(""))
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("ready", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.Done():
	default:
		t.Fatal("Done is open after Close returned")
	}
	if err := term.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

// stuckProgram ignores Stop and never reads, until release is closed.
type stuckProgram struct{ release chan struct{} }

func (p stuckProgram) Run(in io.Reader, out io.Writer, cols, rows int) int {
	io.WriteString(out, "stuck")
	<-p.release
	return 0
}
func (stuckProgram) Resize(int, int) {}
func (stuckProgram) Stop()           {}

// A program that does not return from Run after Stop is reported by Close,
// since its goroutine would outlive the test. A program that stops reading
// makes input time out, as a full PTY does.
func TestProgramThatDoesNotStop(t *testing.T) {
	p := stuckProgram{release: make(chan struct{})}
	defer close(p.release)
	term, err := StartProgram(p, WithKillGrace(100*time.Millisecond), WithWriteTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitForText("stuck", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("x"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Type to a program that never reads: got %v, want a timeout", err)
	}
	err = term.Close()
	if err == nil || !strings.Contains(err.Error(), "did not return") {
		t.Fatalf("Close = %v, want an error saying the program did not return", err)
	}
}

func TestStartProgramNil(t *testing.T) {
	if _, err := StartProgram(nil); err == nil {
		t.Fatal("StartProgram(nil) returned no error")
	}
}
