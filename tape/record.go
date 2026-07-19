package tape

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// DefaultAnchorMax caps how much of a screen line a generated Wait regex uses.
// Waits match as substrings, so a clipped anchor still synchronizes correctly
// while keeping the tape readable.
const DefaultAnchorMax = 48

// minAnchorRunes is the shortest line worth turning into a Wait regex. Shorter
// lines are usually prompts or box-drawing and make brittle anchors.
const minAnchorRunes = 3

// Recorder turns the events of a live session into tape commands. It owns the
// judgement calls that decide whether a pause in the recording becomes a Wait,
// a WaitStable, or a Sleep; the Session type owns the plumbing that feeds it.
//
// The timing policy, in order of preference:
//
//   - the screen changed and the new content has a distinctive line, so the
//     tape waits on that text and is robust to the program being slower or
//     faster on replay;
//   - the screen changed but nothing anchorable appeared, so the tape waits for
//     output to quiesce;
//   - the screen did not change at all, so nothing is emitted, because a
//     human's think-time is not part of the test.
//
// Sleep is only ever emitted as the explicit fallback described by IdleSleep,
// since a tape full of fixed sleeps is exactly the brittle test this harness
// exists to avoid.
type Recorder struct {
	// CaptureSnapshots emits a Snapshot command at every settle point and keeps
	// the screen behind it, so a recording doubles as golden generation.
	CaptureSnapshots bool
	// SnapshotPrefix names the generated snapshots ("step" gives step-01).
	SnapshotPrefix string
	// AnchorMax caps a generated Wait regex; zero means DefaultAnchorMax.
	AnchorMax int
	// IdleSleep, when positive, emits a Sleep for a pause at least this long
	// during which the program produced no output at all. It exists for
	// programs whose behavior genuinely depends on wall-clock delay.
	IdleSleep time.Duration

	dec   inputDecoder
	cmds  []Command
	snapN int
	snaps map[string]string
}

// NewRecorder returns a recorder with the default timing policy.
func NewRecorder() *Recorder {
	return &Recorder{
		SnapshotPrefix: "step",
		AnchorMax:      DefaultAnchorMax,
	}
}

// Header emits the Set and Spawn commands a recording opens with.
func (r *Recorder) Header(cols, rows int, term string, env []string, argv []string) {
	r.cmds = append(r.cmds, Command{
		Kind: KindSet, SetKey: "Size", SetArgs: []string{fmt.Sprint(cols), fmt.Sprint(rows)},
	})
	if term != "" {
		r.cmds = append(r.cmds, Command{Kind: KindSet, SetKey: "Term", SetArgs: []string{term}})
	}
	for _, kv := range env {
		r.cmds = append(r.cmds, Command{Kind: KindSet, SetKey: "Env", SetArgs: []string{kv}})
	}
	r.cmds = append(r.cmds, Command{Kind: KindSpawn, Argv: argv})
}

// Input records a chunk of raw input the user sent to the program.
func (r *Recorder) Input(chunk []byte) {
	r.dec.feed(chunk)
	r.cmds = append(r.cmds, r.dec.take()...)
}

// Resize records a change to the terminal size.
func (r *Recorder) Resize(cols, rows int) {
	r.flushInput()
	r.cmds = append(r.cmds, Command{Kind: KindResize, Cols: cols, Rows: rows})
}

// Settle records that the screen went from before to after and then stopped
// changing, gap after the preceding command. It appends the synchronization
// command the timing policy chose, plus a Snapshot when Snapshots is set.
func (r *Recorder) Settle(before, after string, gap time.Duration) {
	r.flushInput()

	switch {
	case before != after:
		if re, ok := r.anchor(before, after); ok {
			r.cmds = append(r.cmds, Command{
				Kind:     KindWait,
				Regex:    re,
				HasRegex: true,
				Scope:    tuitest.ScopeScreen,
			})
		} else {
			r.cmds = append(r.cmds, Command{Kind: KindWaitStable})
		}
	case r.IdleSleep > 0 && gap >= r.IdleSleep:
		r.cmds = append(r.cmds, Command{Kind: KindSleep, Dur: gap.Round(100 * time.Millisecond)})
	default:
		// The screen never changed, so there is nothing to wait for.
		return
	}

	if r.CaptureSnapshots {
		r.snapN++
		prefix := r.SnapshotPrefix
		if prefix == "" {
			prefix = "step"
		}
		name := fmt.Sprintf("%s-%02d", prefix, r.snapN)
		r.cmds = append(r.cmds, Command{Kind: KindSnapshot, Name: name})
		if r.snaps == nil {
			r.snaps = map[string]string{}
		}
		// after is the same screen text Player.snapshot would capture on
		// replay, so storing it here yields goldens that already agree with the
		// tape rather than needing a second -update pass.
		r.snaps[name] = after
	}
}

// SnapshotFiles returns the captured screens keyed by snapshot name, ready to be
// written as golden files. It is empty unless CaptureSnapshots was set.
func (r *Recorder) SnapshotFiles() map[string]string { return r.snaps }

// ExpectExit records that the program exited with the given code.
func (r *Recorder) ExpectExit(code int) {
	r.flushInput()
	r.cmds = append(r.cmds, Command{Kind: KindExpectExit, Code: code})
}

// Commands returns the recorded tape, flushing anything still buffered.
func (r *Recorder) Commands() []Command {
	r.dec.close()
	r.cmds = append(r.cmds, r.dec.take()...)
	return r.cmds
}

// flushInput finalizes any input still being accumulated, so that commands
// emitted after it land in the right order.
func (r *Recorder) flushInput() {
	r.dec.flush()
	r.cmds = append(r.cmds, r.dec.take()...)
}

// SetModes tells the recorder the terminal modes the program under test has
// negotiated, so input is decoded the way the program will interpret it. The
// same bytes are a different key under different modes, which is why this
// cannot be inferred from the input stream alone.
func (r *Recorder) SetModes(m Modes) { r.dec.setModes(m) }

// anchor picks the line of after to wait on: the last line that is genuinely
// new, long enough to be distinctive, and that does not already match before.
// The last condition is what makes the wait meaningful, since a pattern already
// on screen would pass instantly and synchronize nothing.
func (r *Recorder) anchor(before, after string) (*regexp.Regexp, bool) {
	prior := map[string]bool{}
	for _, line := range strings.Split(before, "\n") {
		prior[strings.TrimSpace(line)] = true
	}

	limit := r.AnchorMax
	if limit <= 0 {
		limit = DefaultAnchorMax
	}

	lines := strings.Split(after, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		cand := strings.TrimSpace(lines[i])
		if prior[cand] {
			continue
		}
		if len([]rune(strings.ReplaceAll(cand, " ", ""))) < minAnchorRunes {
			continue
		}
		re, err := anchorRegex(clipRunes(cand, limit))
		if err != nil || re.MatchString(before) {
			continue
		}
		return re, true
	}
	return nil, false
}

// anchorRegex turns a screen line into the pattern a tape waits on. Runs of
// whitespace become \s+ rather than literal spaces, which does two jobs: the
// pattern tolerates the program padding a line differently on replay, and the
// emitted regex contains no spaces, so it survives the tape's whitespace-
// delimited tokenizer byte for byte.
func anchorRegex(line string) (*regexp.Regexp, error) {
	fields := strings.Fields(line)
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, regexp.QuoteMeta(f))
	}
	return regexp.Compile(strings.Join(quoted, `\s+`))
}

// clipRunes shortens s to at most n runes, never splitting a rune.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
