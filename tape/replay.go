package tape

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Replayer runs a tape and renders the program to a real terminal as it goes,
// so a failing assertion can be watched rather than deduced. It wraps Player
// rather than reimplementing it, so what you watch is exactly what a headless
// run does.
type Replayer struct {
	// Render receives the program's output, normally the operator's terminal.
	Render io.Writer
	// Log receives the command trace and any failure report.
	Log io.Writer
	// Speed divides every Sleep; 2 is twice as fast. Zero means unscaled.
	Speed float64
	// Echo prints each command before it runs.
	Echo bool
	// Step pauses before each command until a line is read from StepIn.
	Step bool
	// StepIn supplies the keypresses that advance step mode.
	StepIn io.Reader
	// Width is the column width used for side-by-side failure output.
	Width int

	// GoldenDir, Update, and Strict are handed to the underlying Player.
	GoldenDir string
	Update    bool
	Strict    bool
}

// ErrStepAborted is returned when the operator ends a stepped replay early.
var ErrStepAborted = errors.New("tape: replay aborted")

// Run replays the tape. On a failed assertion it writes a report showing the
// expected and actual screens before returning the error.
func (r *Replayer) Run(cmds []Command) error {
	logw := r.Log
	if logw == nil {
		logw = io.Discard
	}

	p := NewPlayer()
	if r.GoldenDir != "" {
		p.GoldenDir = r.GoldenDir
	}
	p.Update = r.Update
	p.Strict = r.Strict
	p.SleepScale = r.Speed
	p.Mirror = r.Render

	var stepReader *bufio.Reader
	if r.Step && r.StepIn != nil {
		stepReader = bufio.NewReader(r.StepIn)
	}

	p.Before = func(c Command) error {
		if r.Echo {
			fmt.Fprintf(logw, "\r\n> %s\r\n", c.String())
		}
		if stepReader != nil {
			fmt.Fprint(logw, "\r[enter to step, q to quit] ")
			line, err := stepReader.ReadString('\n')
			if err != nil && line == "" {
				return ErrStepAborted
			}
			if strings.HasPrefix(strings.TrimSpace(line), "q") {
				return ErrStepAborted
			}
		}
		return nil
	}

	err := p.Run(cmds)
	if err != nil && !errors.Is(err, ErrStepAborted) {
		fmt.Fprint(logw, "\r\n"+r.report(err)+"\r\n")
	}
	return err
}

// report renders a failure so the operator sees the frame that went wrong. A
// golden mismatch has two screens and gets a side-by-side; an Expect failure has
// a pattern and a screen, which do not line up in columns and so are stacked.
func (r *Replayer) report(err error) string {
	width := r.Width
	if width <= 0 {
		width = 40
	}

	var snap *SnapshotError
	if errors.As(err, &snap) {
		return fmt.Sprintf("FAIL snapshot %q (%s)\n\n%s",
			snap.Name, snap.Path,
			SideBySide("expected (golden)", snap.Want, "actual (screen)", snap.Got, width))
	}

	var exp *ExpectError
	if errors.As(err, &exp) {
		return fmt.Sprintf("FAIL expect /%s/\n\n--- screen ---\n%s", exp.Regex, exp.Screen)
	}

	return "FAIL " + err.Error()
}
