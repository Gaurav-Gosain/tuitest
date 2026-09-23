package fixtures_test

import (
	"fmt"

	"github.com/Gaurav-Gosain/tuitest/fixtures"
)

func ExampleFakeShell() {
	sh := fixtures.NewFakeShell()
	defer sh.Close() //nolint:errcheck

	// The code under test writes input to the shell...
	_, _ = sh.Write([]byte("ls\r"))
	fmt.Printf("%q\n", sh.GetInput())

	// ...and reads back whatever the test queued as the shell's output.
	sh.SendOutput(fixtures.LSOutput([]string{"src", "go.mod"}, []bool{true, false}))
	buf := make([]byte, 128)
	n, _ := sh.Read(buf)
	fmt.Printf("%q\n", buf[:n])
	// Output:
	// "ls\r"
	// "\x1b[34m\x1b[1msrc\x1b[0m  go.mod  \r\n"
}

func ExampleANSIBuilder() {
	seq := fixtures.NewANSIBuilder().
		AltScreen().
		CursorTo(1, 1).
		Bold().Text("title").Reset().
		String()
	fmt.Printf("%q\n", seq)
	// Output:
	// "\x1b[?1049h\x1b[1;1H\x1b[1mtitle\x1b[0m"
}
