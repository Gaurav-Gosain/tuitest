package cli

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/fang"
	xterm "github.com/charmbracelet/x/term"
)

// This file owns how a failure looks. tuitest's errors are not one-liners: a
// wait that times out reports what it waited for, how long it waited, and the
// screen as it was at that moment, and an assertion failure contrasts two
// screens column by column. That text is the product, and it only works if it
// reaches the terminal with its line structure intact.
//
// fang's default renderer reflows an error into a single width-bounded
// paragraph, which turns a screen dump into a wall of words. So tuitest renders
// its own errors here and hands fang only the errors it raises itself: an
// unknown command, an unknown flag, a bad flag value. Those are one line each
// and lose nothing by being reflowed.

// diagnosticErrorHandler is the handler fang is given. The errors it actually
// sees are the ones cobra raises during parsing and dispatch, which are single
// line and are best served by fang's own styling, so it delegates. It exists as
// a named handler rather than being left at the default so that a multi-line
// error never silently starts being reflowed if one does reach fang.
func diagnosticErrorHandler(w io.Writer, styles fang.Styles, err error) {
	if !strings.Contains(err.Error(), "\n") {
		fang.DefaultErrorHandler(w, styles, err)
		return
	}
	writeError(w, errorStyles(), err.Error())
}

// reportError prints a command failure to the Env's stderr.
//
// The rendered text is what the flag-based CLI printed, prefix and all, because
// it is already the right text: the library prefixes its errors with "tuitest:"
// so they stand out in Go test output, render strips that, and the CLI prints
// the program name once itself.
func reportError(env *Env, err error) {
	if err == nil {
		return
	}
	msg := "tuitest: " + render(err)
	if !isTerminal(env.Stderr) {
		// Match the non-tty behaviour of fang's default handler: no styling,
		// no decoration, so a CI log and a piped stderr stay parseable, and so
		// a test that captures the buffer sees exactly the bytes a script sees.
		fmt.Fprintln(env.Stderr, msg)
		return
	}
	writeError(env.Stderr, errorStyles(), msg)
}

// writeError prints a message under an ERROR header, one styled line per source
// line, so nothing is reflowed.
func writeError(w io.Writer, styles fang.Styles, msg string) {
	fmt.Fprintln(w, styles.ErrorHeader.String())
	for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
		fmt.Fprintln(w, styles.ErrorText.UnsetTransform().Render(line))
	}
	fmt.Fprintln(w)
}

// errorStyles builds the styles the error renderer needs without asking the
// terminal anything.
//
// This exists because of how long fang takes to answer that question itself. To
// pick colors it calls lipgloss.HasDarkBackground, which writes an OSC 11 query
// and waits up to two seconds for a reply, against stdin and then stdout: four
// seconds when nothing answers. Terminals that do not implement the query and
// multiplexers that swallow it both fail to answer, and tuitest is a tool whose
// stdin is routinely something other than a terminal, so this is not a rare
// case. A tool CI runs on every push cannot spend four seconds deciding what
// color to print a failure in.
//
// Fixed ANSI red-on-black needs no query and is legible on a light or a dark
// background, so nothing is lost by not asking.
func errorStyles() fang.Styles {
	return fang.Styles{
		// No Width is set, and that is the point: lipgloss wraps to a set
		// width, and a screen dump wrapped at the terminal's width is no
		// longer a picture of the screen. Long lines are left to the terminal
		// to wrap, which at least keeps the columns aligned when it does not
		// have to.
		ErrorText: lipgloss.NewStyle().MarginLeft(2),
		ErrorHeader: lipgloss.NewStyle().
			Foreground(lipgloss.Black).
			Background(lipgloss.Red).
			Bold(true).
			Padding(0, 1).
			Margin(1).
			MarginLeft(2).
			SetString("ERROR"),
	}
}

// isTerminal reports whether w is a terminal, which is false for the buffers a
// test supplies and for a redirected stderr.
func isTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && xterm.IsTerminal(f.Fd())
}
