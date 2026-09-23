package tuitest_test

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// The examples drive sh, so they run anywhere tuitest does. Every one of them
// is executed by go test and its output checked, so they cannot drift from the
// API they document.

// greeter is a tiny interactive program: it asks for a name and answers.
var greeter = []string{"sh", "-c", `printf 'name? '; read name; printf 'hello, %s\n' "$name"`}

// Start a program, type into it, wait for its answer, and read the screen.
//
// "gopher" is not on the screen. tuitest turns the PTY's echo off before the
// program starts, so the screen holds only what the program drew, and a
// line-oriented program that relies on the terminal to echo its input shows
// none. A TUI draws its own input, so this only shows up with programs like
// this one. See docs/limits.md.
func Example() {
	term, err := tuitest.Start(greeter, tuitest.WithSize(40, 5))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	if err := term.WaitForText("name?", 5*time.Second); err != nil {
		fmt.Println(err)
		return
	}
	_ = term.SendKeys("gopher", tuitest.Enter)
	if err := term.WaitForText("hello, gopher", 5*time.Second); err != nil {
		fmt.Println(err)
		return
	}
	code, _ := term.WaitExit(5 * time.Second)

	fmt.Println(term.Snapshot())
	fmt.Println("exit", code)
	// Output:
	// name? hello, gopher
	// exit 0
}

// StartT is Start for use inside a test: it fails the test if the spawn fails,
// mirrors PTY traffic into t.Log, and closes the terminal in t.Cleanup.
func ExampleStartT() {
	test := func(t *testing.T) {
		term := tuitest.StartT(t, greeter, tuitest.WithSize(40, 5))
		if err := term.WaitForText("name?", 5*time.Second); err != nil {
			t.Fatal(err)
		}
		_ = term.SendKeys("gopher", tuitest.Enter)
		if err := term.WaitForText("hello, gopher", 5*time.Second); err != nil {
			t.Fatal(err)
		}
		// Compare against testdata/greeting.golden; UPDATE_GOLDEN=1 writes it.
		term.AssertGolden(t, "greeting")
	}
	_ = test
}

// A wait that runs out of time says what it waited for and carries the screen,
// and errors.Is tells a timeout apart from the program exiting early.
func ExampleTerminal_WaitForText() {
	term, err := tuitest.Start([]string{"sh", "-c", "echo starting; sleep 5"}, tuitest.WithSize(40, 3))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	if err := term.WaitForText("starting", 5*time.Second); err != nil {
		fmt.Println(err)
		return
	}
	err = term.WaitForText("ready", 200*time.Millisecond)
	fmt.Println(errors.Is(err, tuitest.ErrTimeout))

	var te *tuitest.TimeoutError
	if errors.As(err, &te) {
		fmt.Println(te.Screen)
	}
	// Output:
	// true
	// starting
}

// WaitForMatch takes a regexp, and ScopeLastLine restricts it to the last
// non-blank row, which is where a prompt or a status line usually is.
func ExampleTerminal_WaitForMatch() {
	term, err := tuitest.Start([]string{"sh", "-c", "echo 'build 1 of 3'; echo 'done in 42ms'; sleep 5"}, tuitest.WithSize(40, 5))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	re := regexp.MustCompile(`done in \d+ms`)
	if err := term.WaitForMatch(re, tuitest.ScopeLastLine, 5*time.Second); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(re.FindString(term.Screen().Text()))
	// Output:
	// done in 42ms
}

// WaitFor takes any condition on the screen. Here it waits for a cell to be
// drawn bold, which no text match can see.
func ExampleTerminal_WaitFor() {
	term, err := tuitest.Start([]string{"sh", "-c", `printf 'plain \033[1mBOLD\033[0m'; sleep 5`}, tuitest.WithSize(40, 3))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	err = term.WaitFor(func(s tuitest.Screen) bool {
		return s.Cell(6, 0).Bold
	}, 5*time.Second)
	fmt.Println(err, term.Screen().Cell(6, 0).Content, term.Screen().Cell(0, 0).Bold)
	// Output:
	// <nil> B false
}

// Resize changes the PTY size, so the program receives SIGWINCH and sees the
// new size, and resizes the emulator grid to match.
func ExampleTerminal_Resize() {
	term, err := tuitest.Start([]string{"sh", "-c", "stty size; read x; stty size; sleep 5"}, tuitest.WithSize(80, 24))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	_ = term.WaitForText("24 80", 5*time.Second)
	_ = term.Resize(50, 10)
	_ = term.SendKeys(tuitest.Enter)
	_ = term.WaitForText("10 50", 5*time.Second)

	cols, rows := term.Screen().Size()
	fmt.Println(cols, rows)
	fmt.Println(term.Snapshot())
	// Output:
	// 50 10
	// 24 80
	// 10 50
}

// Paste wraps text in bracketed-paste markers, the way a terminal delivers a
// real paste. cat -v makes the markers visible.
func ExampleTerminal_Paste() {
	term, err := tuitest.Start([]string{"sh", "-c", `echo ready; IFS= read -r line; printf '%s\n' "$line" | cat -v; sleep 5`}, tuitest.WithSize(40, 3))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	_ = term.WaitForText("ready", 5*time.Second)
	_ = term.Paste("hi")
	_ = term.SendKeys(tuitest.Enter)
	_ = term.WaitForText("201~", 5*time.Second)
	fmt.Println(term.Screen().Line(1))
	// Output:
	// ^[[200~hi^[[201~
}

// TermState reports the modes a program left set. A program that exits on the
// alternate screen leaves the user's shell unusable, and Dirty catches it.
func ExampleTerminal_TermState() {
	term, err := tuitest.Start([]string{"sh", "-c", `printf '\033[?1049h\033[?25l'`}, tuitest.WithSize(40, 3))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	_, _ = term.WaitExit(5 * time.Second)
	st := term.TermState()
	fmt.Println(st.Dirty(), st.AltScreen, st.CursorHidden)
	// Output:
	// true true true
}

// ExitStatus separates a signal death from an ordinary non-zero exit, which
// ExitCode flattens to -1.
func ExampleTerminal_ExitStatus() {
	term, err := tuitest.Start([]string{"sh", "-c", "kill -SEGV $$"}, tuitest.WithSize(40, 3))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	_, _ = term.WaitExit(5 * time.Second)
	st, _ := term.ExitStatus()
	fmt.Println(st.Signaled, st.Crashed(), st)
	// Output:
	// true true killed by segmentation fault
}

// WaitForPrompt and WaitForCommand follow a shell through OSC 133 semantic
// markers, which the shell (or its prompt framework) has to emit.
//
// Both wait for a marker newer than the call, so a marker that arrives before
// the wait starts is not seen. The script sleeps before each one to keep this
// example deterministic; a real shell is slower than the Go side anyway.
func ExampleTerminal_WaitForCommand() {
	// A stand-in for a shell with shell integration: prompt, command, exit 7.
	script := `sleep 0.2; printf '\033]133;A\007$ \033]133;B\007'; read x; sleep 0.2; printf '\033]133;C\007ran\n\033]133;D;7\007'; sleep 5`
	term, err := tuitest.Start([]string{"sh", "-c", script}, tuitest.WithSize(40, 5), tuitest.WithSemanticMarkers())
	if err != nil {
		fmt.Println(err)
		return
	}
	defer term.Close() //nolint:errcheck

	if err := term.WaitForPrompt(5 * time.Second); err != nil {
		fmt.Println(err)
		return
	}
	_ = term.SendKeys(tuitest.Enter)
	if err := term.WaitForCommand(5 * time.Second); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(term.LastCommandExit())
	// Output:
	// 7 true
}

// Ctrl and Alt build chords as the bytes a terminal sends for them.
func ExampleCtrl() {
	fmt.Printf("%q %q %q\n", tuitest.Ctrl('c'), tuitest.Alt('x'), tuitest.Alt(tuitest.Enter))
	// Output:
	// "\x03" "\x1bx" "\x1b\r"
}

// Diff is the line diff AssertGolden prints on a mismatch.
func ExampleDiff() {
	fmt.Print(tuitest.Diff("title\nok\n", "title\nfail\n"))
}
