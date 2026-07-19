package fuzz

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gaurav-Gosain/tuitest"
	"github.com/Gaurav-Gosain/tuitest/tape"
)

// Navigation and editing keys a TUI is expected to handle. These drive the
// program through its own state machine rather than just filling a text field,
// which is what makes the input structured instead of noise.
var navKeys = []string{
	"Up", "Down", "Left", "Right",
	"Home", "End", "PageUp", "PageDown",
	"Enter", "Tab", "Esc", "Backspace", "Delete", "Insert", "Space",
}

var functionKeys = []string{
	"F1", "F2", "F3", "F4", "F5", "F6",
	"F7", "F8", "F9", "F10", "F11", "F12",
}

// Control chords. Ctrl+c is included on purpose even though it usually quits:
// the terminal-restoration check can only run on a program that has exited, so
// a generator that never asks a program to quit can never find the single most
// common TUI bug. It is one chord among many, so a run still explores the
// program's interior first, and -exclude turns it off for programs where
// quitting early is unhelpful.
//
// Ctrl+z is omitted entirely: suspending the child under a PTY wedges the run
// rather than finding anything about the program.
var ctrlKeys = []string{
	"Ctrl+a", "Ctrl+b", "Ctrl+c", "Ctrl+e", "Ctrl+f", "Ctrl+g", "Ctrl+h",
	"Ctrl+k", "Ctrl+l", "Ctrl+n", "Ctrl+o", "Ctrl+p", "Ctrl+q",
	"Ctrl+r", "Ctrl+s", "Ctrl+t", "Ctrl+u", "Ctrl+v", "Ctrl+w",
	"Ctrl+x", "Ctrl+y",
}

var altKeys = []string{
	"Alt+a", "Alt+b", "Alt+f", "Alt+d", "Alt+x", "Alt+Enter", "Alt+Tab",
}

// Text fragments mixing ASCII, accented Latin, CJK (double-width), emoji
// (double-width, multi-codepoint), combining marks, and zero-width joiners.
// Width handling is where TUI layout code most often goes wrong.
var textFragments = []string{
	"hello", "world", "test", "abc", "0123456789",
	"the quick brown fox", "  leading spaces", "trailing spaces  ",
	"café", "naïve", "über",
	"你好", "こんにちは", "한글",
	"\U0001f600", "\U0001f468‍\U0001f469‍\U0001f466", "\U0001f1ef\U0001f1f5",
	"é", "à́̂",
	"​", "‎", "‮",
	"tab\there", "line\nbreak",
	strings.Repeat("x", 200),
}

// Sizes worth resizing to. Degenerate sizes are over-represented on purpose:
// one column, one row, and very large dimensions are where layout arithmetic
// divides by zero or indexes out of range.
var degenerateSizes = [][2]int{
	{1, 1}, {1, 24}, {80, 1}, {2, 2}, {1, 2}, {2, 1},
	{3, 3}, {5, 2}, {500, 1}, {1, 500}, {1000, 1000},
	{80, 24}, {120, 40}, {40, 12}, {200, 60},
}

// resizeRedrawWait bounds how long a generated run pauses after a resize for
// the program to repaint.
const resizeRedrawWait = 200 * time.Millisecond

// Config controls how input is generated.
type Config struct {
	// Cols and Rows are the size the program is spawned at.
	Cols, Rows int
	// ActionsPerRun bounds how many commands one iteration sends.
	ActionsPerRun int
	// ExcludeKeys lists key tokens the generator must never emit, for programs
	// where a key legitimately quits and would cut every run short.
	ExcludeKeys []string
	// NoHostile disables malformed and oversized escape sequences, leaving only
	// well-formed input.
	NoHostile bool
	// NoResize disables resize actions.
	NoResize bool
	// NoMouse disables mouse actions.
	NoMouse bool
}

func (c Config) withDefaults() Config {
	if c.Cols <= 0 {
		c.Cols = 80
	}
	if c.Rows <= 0 {
		c.Rows = 24
	}
	if c.ActionsPerRun <= 0 {
		c.ActionsPerRun = 60
	}
	return c
}

// generator produces tape commands from a seeded PRNG. It emits tape.Command
// values rather than a private action type so that everything the fuzzer does
// is expressible as a tape line, and a repro therefore replays through the same
// player that drove the original run.
type generator struct {
	cfg  Config
	rand *rand.Rand
	// cols and rows track the size the program currently believes it has, so
	// mouse coordinates can be aimed inside (and deliberately outside) it.
	cols, rows int
	excluded   map[string]bool
}

func newGenerator(cfg Config, seed uint64) *generator {
	cfg = cfg.withDefaults()
	excluded := make(map[string]bool, len(cfg.ExcludeKeys))
	for _, k := range cfg.ExcludeKeys {
		excluded[strings.TrimSpace(k)] = true
	}
	return &generator{
		cfg: cfg,
		// PCG seeded from the run seed: the same seed replays the same run.
		rand:     rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		cols:     cfg.Cols,
		rows:     cfg.Rows,
		excluded: excluded,
	}
}

// Run generates one complete iteration: a Spawn followed by input commands.
func (g *generator) Run(argv []string) []tape.Command {
	cmds := []tape.Command{
		{Kind: tape.KindSet, SetKey: "Size", SetArgs: []string{strconv.Itoa(g.cfg.Cols), strconv.Itoa(g.cfg.Rows)}},
		{Kind: tape.KindSpawn, Argv: argv},
		// Wait for the program to draw its first frame before poking it.
		{Kind: tape.KindWaitOutput},
		{Kind: tape.KindWaitStable},
	}
	g.cols, g.rows = g.cfg.Cols, g.cfg.Rows
	n := 1 + g.rand.IntN(g.cfg.ActionsPerRun)
	for i := 0; i < n; i++ {
		cmds = append(cmds, g.action()...)
	}
	return cmds
}

// action returns the command or commands for one randomly chosen input event.
// The weights favour ordinary interaction, because a bug reached through a
// plausible sequence of real input is worth more than one reached by byte noise
// alone. Some events expand to several commands: a drag is a press, a run of
// moves, and a release, which only means anything as a sequence.
func (g *generator) action() []tape.Command {
	switch g.pick() {
	case actText:
		return []tape.Command{{Kind: tape.KindRaw, Text: g.text()}}
	case actKey:
		k, ok := g.key()
		if !ok {
			return nil
		}
		return []tape.Command{{Kind: tape.KindKey, Keys: []string{k}}}
	case actPaste:
		return []tape.Command{{Kind: tape.KindPaste, Text: g.pasteBurst()}}
	case actMouse:
		return g.mouseCommands()
	case actResize:
		cols, rows := g.size()
		g.cols, g.rows = cols, rows
		return []tape.Command{
			{Kind: tape.KindResize, Cols: cols, Rows: rows},
			// Give the program a moment to redraw before sending more. Without
			// this the redraw lands after the next few inputs and makes a
			// program that wedged on the resize look like it answered them,
			// which is exactly how a real hang goes unreported. The bound is
			// short because a program that ignores resizes would otherwise pay
			// the full settle timeout on every one.
			{Kind: tape.KindWaitOutput, Timeout: resizeRedrawWait, HasTimeout: true},
		}
	case actHostile:
		return []tape.Command{{Kind: tape.KindRaw, Text: hostile(g.rand)}}
	default:
		// Wait for the program to react to what came before. WaitOutput rather
		// than WaitStable: after a pause the screen is already stable, so
		// WaitStable would return without the program having done anything.
		return []tape.Command{{Kind: tape.KindWaitOutput}}
	}
}

// mouseCommands emits either a single click or wheel event, or a full drag.
func (g *generator) mouseCommands() []tape.Command {
	if g.rand.IntN(3) != 0 {
		return []tape.Command{{Kind: tape.KindMouse, Mouse: g.mouse()}}
	}

	// A drag: press, several moves with the button held, then release. The
	// moves walk from the press point so the path is coherent rather than
	// teleporting, which is what a real drag looks like to the program.
	start := g.mouse()
	start.Action = tuitest.MousePress
	if start.Button > tuitest.MouseRight {
		start.Button = tuitest.MouseLeft
	}

	cmds := []tape.Command{{Kind: tape.KindMouse, Mouse: start}}
	col, row := start.Col, start.Row
	steps := 1 + g.rand.IntN(6)
	for i := 0; i < steps; i++ {
		col += g.rand.IntN(7) - 3
		row += g.rand.IntN(5) - 2
		mv := start
		mv.Action = tuitest.MouseMove
		mv.Col, mv.Row = max(col, 0), max(row, 0)
		cmds = append(cmds, tape.Command{Kind: tape.KindMouse, Mouse: mv})
	}
	end := start
	end.Action = tuitest.MouseRelease
	end.Col, end.Row = max(col, 0), max(row, 0)
	return append(cmds, tape.Command{Kind: tape.KindMouse, Mouse: end})
}

type actionKind int

const (
	actText actionKind = iota
	actKey
	actPaste
	actMouse
	actResize
	actHostile
	actSettle
)

// pick chooses an action kind by weight, skipping kinds the config disabled.
func (g *generator) pick() actionKind {
	type weighted struct {
		kind   actionKind
		weight int
	}
	table := []weighted{
		{actText, 25},
		{actKey, 35},
		{actPaste, 5},
		{actSettle, 3},
	}
	if !g.cfg.NoMouse {
		table = append(table, weighted{actMouse, 12})
	}
	if !g.cfg.NoResize {
		table = append(table, weighted{actResize, 8})
	}
	if !g.cfg.NoHostile {
		table = append(table, weighted{actHostile, 12})
	}

	total := 0
	for _, w := range table {
		total += w.weight
	}
	n := g.rand.IntN(total)
	for _, w := range table {
		if n < w.weight {
			return w.kind
		}
		n -= w.weight
	}
	return actSettle
}

func (g *generator) text() string {
	s := textFragments[g.rand.IntN(len(textFragments))]
	// Occasionally truncate mid-rune, which produces invalid UTF-8 from
	// otherwise valid text: a realistic way for bad bytes to reach a program.
	if g.rand.IntN(10) == 0 && len(s) > 2 {
		cut := 1 + g.rand.IntN(len(s)-1)
		if !utf8.ValidString(s[:cut]) || cut < len(s) {
			return s[:cut]
		}
	}
	return s
}

// key returns a key token, honouring the exclusion list. It reports false when
// the chosen key is excluded, and the caller simply drops that action.
//
// The pools are sampled fairly evenly rather than heavily favouring navigation.
// A bug bound to one specific key is only found if that key is actually sent,
// and a distribution that reaches a twelve-key pool one time in ten needs an
// order of magnitude more iterations to reach any single member of it.
func (g *generator) key() (string, bool) {
	var pool []string
	switch n := g.rand.IntN(20); {
	case n < 8:
		pool = navKeys
	case n < 13:
		pool = ctrlKeys
	case n < 17:
		pool = functionKeys
	default:
		pool = altKeys
	}
	k := pool[g.rand.IntN(len(pool))]
	if g.excluded[k] {
		return "", false
	}
	return k, true
}

func (g *generator) pasteBurst() string {
	// Paste bursts are deliberately large: a program that handles typing one
	// rune at a time often has an unbounded or quadratic path for pasted input.
	n := 1 + g.rand.IntN(40)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(textFragments[g.rand.IntN(len(textFragments))])
		if g.rand.IntN(4) == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (g *generator) mouse() tuitest.MouseEvent {
	buttons := []tuitest.MouseButton{
		tuitest.MouseLeft, tuitest.MouseMiddle, tuitest.MouseRight,
		tuitest.MouseWheelUp, tuitest.MouseWheelDown,
	}
	actions := []tuitest.MouseAction{
		tuitest.MousePress, tuitest.MouseRelease, tuitest.MouseMove,
	}
	col, row := g.rand.IntN(max(g.cols, 1)), g.rand.IntN(max(g.rows, 1))
	// One event in eight lands outside the current grid. Real terminals do
	// deliver these during a drag that leaves the window, and coordinate
	// handling that assumes in-bounds is a common source of panics.
	if g.rand.IntN(8) == 0 {
		col, row = col+g.cols, row+g.rows
	}
	return tuitest.MouseEvent{
		Col:    col,
		Row:    row,
		Button: buttons[g.rand.IntN(len(buttons))],
		Action: actions[g.rand.IntN(len(actions))],
		Mods:   tuitest.KeyMods(g.rand.IntN(8)),
	}
}

func (g *generator) size() (int, int) {
	// Two thirds of resizes come from the degenerate table, since that is where
	// the bugs are; the rest are arbitrary sizes in a plausible range.
	if g.rand.IntN(3) < 2 {
		s := degenerateSizes[g.rand.IntN(len(degenerateSizes))]
		return s[0], s[1]
	}
	return 1 + g.rand.IntN(300), 1 + g.rand.IntN(100)
}
