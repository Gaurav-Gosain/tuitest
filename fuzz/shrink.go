package fuzz

import (
	"context"
	"strings"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// shrink minimises a failing command sequence while it keeps producing the same
// kind of failure. It runs two passes to exhaustion: removing commands, then
// simplifying the ones that remain.
//
// Removal comes first and in decreasing chunk sizes, the classic delta
// debugging shape. Large chunks are tried early because a fuzz run is mostly
// irrelevant input, so the cheapest big win is deleting half of it; the pass
// then narrows until it is removing single commands. Simplification runs
// afterwards on the much shorter sequence that survives, where each candidate
// replay is cheap.
//
// Every candidate costs a full spawn-and-drive, so the budget is a hard cap on
// replays rather than on passes.
func shrink(ctx context.Context, opts Options, f *Failure) *Failure {
	return shrinkUsing(ctx, opts, f, func(candidate []tape.Command) bool {
		return reproduces(ctx, opts, candidate, f.Kind)
	})
}

// shrinkUsing is shrink with the "does this still fail?" test injected. The
// production caller replays the candidate against the real program; tests
// supply a predicate instead, so the search strategy can be exercised without
// spawning anything and without depending on a program's timing.
func shrinkUsing(ctx context.Context, opts Options, f *Failure, stillFails func([]tape.Command) bool) *Failure {
	budget := opts.ShrinkBudget
	best := f.Commands

	spend := func() bool {
		if budget <= 0 {
			return false
		}
		budget--
		return true
	}

	// Pass one: delete chunks, halving the chunk size each time a full sweep
	// finds nothing more to remove.
	for chunk := len(best) / 2; chunk >= 1; chunk /= 2 {
		progress := true
		for progress {
			progress = false
			for i := 0; i+chunk <= len(best); {
				if ctx.Err() != nil || budget <= 0 {
					return finish(f, best)
				}
				candidate := removeRange(best, i, i+chunk)
				if !required(candidate) {
					i += chunk
					continue
				}
				if !spend() {
					return finish(f, best)
				}
				if stillFails(candidate) {
					best = candidate
					progress = true
					// Do not advance i: the next chunk has shifted into place.
					continue
				}
				i += chunk
			}
		}
		if chunk == 1 {
			break
		}
	}

	// Pass two: simplify individual commands. Each command has a small ladder
	// of strictly simpler forms; the first that still reproduces wins.
	for i := range best {
		for _, simpler := range simplifications(best[i]) {
			if ctx.Err() != nil || budget <= 0 {
				return finish(f, best)
			}
			candidate := replaceAt(best, i, simpler)
			if !spend() {
				return finish(f, best)
			}
			if stillFails(candidate) {
				best = candidate
				break
			}
		}
	}

	return finish(f, best)
}

// finish returns f with the minimised commands attached, preserving the screen
// and detail from the original observation.
func finish(f *Failure, best []tape.Command) *Failure {
	out := *f
	out.Commands = best
	return &out
}

// required reports whether a candidate still has the scaffolding it needs to be
// runnable at all. Removing the Spawn would make every later command fail for
// an uninteresting reason, so those candidates are rejected without spending a
// replay on them.
func required(cmds []tape.Command) bool {
	for _, c := range cmds {
		if c.Kind == tape.KindSpawn {
			return true
		}
	}
	return false
}

func removeRange(cmds []tape.Command, from, to int) []tape.Command {
	out := make([]tape.Command, 0, len(cmds)-(to-from))
	out = append(out, cmds[:from]...)
	return append(out, cmds[to:]...)
}

func replaceAt(cmds []tape.Command, i int, c tape.Command) []tape.Command {
	out := make([]tape.Command, len(cmds))
	copy(out, cmds)
	out[i] = c
	return out
}

// simplifications returns strictly simpler forms of a command, in the order
// they should be tried: simplest first, so the search settles on the smallest
// form that still reproduces.
func simplifications(c tape.Command) []tape.Command {
	switch c.Kind {
	case tape.KindRaw, tape.KindPaste:
		return shrinkText(c)
	case tape.KindResize:
		return shrinkResize(c)
	case tape.KindMouse:
		return shrinkMouse(c)
	default:
		return nil
	}
}

// shrinkText tries progressively longer prefixes of a payload. A hostile burst
// that breaks a parser usually does so on a short prefix, and reporting the
// shortest one tells the author exactly which bytes matter.
func shrinkText(c tape.Command) []tape.Command {
	s := c.Text
	if len(s) <= 1 {
		return nil
	}
	var out []tape.Command
	seen := map[int]bool{}
	// Try an empty payload, then a single byte, then halves, then three
	// quarters: a coarse ladder that finds the boundary in a few replays
	// instead of a linear scan.
	for _, n := range []int{0, 1, len(s) / 8, len(s) / 4, len(s) / 2, len(s) * 3 / 4} {
		if n < 0 || n >= len(s) || seen[n] {
			continue
		}
		seen[n] = true
		next := c
		next.Text = s[:n]
		out = append(out, next)
	}
	// Also try collapsing a long run of one repeated byte to a short run, which
	// is the shape most generated hostile payloads have.
	if r, n := singleRun(s); n > 4 {
		next := c
		next.Text = strings.Repeat(string(r), 4)
		out = append(out, next)
	}
	return out
}

// singleRun reports the byte a string consists of, if it is one byte repeated.
func singleRun(s string) (byte, int) {
	if s == "" {
		return 0, 0
	}
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return 0, 0
		}
	}
	return s[0], len(s)
}

// shrinkResize walks a size back toward the ordinary default, so a repro says
// "it breaks at one column" rather than "it breaks at 1x500".
func shrinkResize(c tape.Command) []tape.Command {
	candidates := [][2]int{{80, 24}, {1, 1}, {1, 24}, {80, 1}, {40, 12}}
	var out []tape.Command
	for _, s := range candidates {
		if s[0] == c.Cols && s[1] == c.Rows {
			continue
		}
		// Only accept a candidate that is genuinely no larger, so the pass
		// cannot loop by growing a size back up.
		if s[0]*s[1] > c.Cols*c.Rows {
			continue
		}
		next := c
		next.Cols, next.Rows = s[0], s[1]
		out = append(out, next)
	}
	return out
}

// shrinkMouse drops modifiers and pulls the event toward the origin.
func shrinkMouse(c tape.Command) []tape.Command {
	var out []tape.Command
	if c.Mouse.Mods != 0 {
		next := c
		next.Mouse.Mods = 0
		out = append(out, next)
	}
	if c.Mouse.Col != 0 || c.Mouse.Row != 0 {
		next := c
		next.Mouse.Col, next.Mouse.Row = 0, 0
		out = append(out, next)
	}
	return out
}

// reproduces replays a candidate and reports whether it fails the same way.
// Matching on kind rather than on the exact detail string is deliberate: the
// cursor coordinates in a message change as input shrinks, and demanding an
// exact match would reject every genuine reduction.
func reproduces(ctx context.Context, opts Options, cmds []tape.Command, want FailureKind) bool {
	f := drive(ctx, opts, cmds)
	return f != nil && f.Kind == want
}
