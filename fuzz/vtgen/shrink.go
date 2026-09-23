package vtgen

import (
	"strconv"
	"strings"
)

// Shrinking is what decides whether this generator is worth having. A failing
// script is a few hundred sequences of noise around the two that matter, and
// nobody reads that. The passes below run to a fixpoint: drop a block, drop a
// single step, then simplify what is left in place, stopping when a whole round
// changes nothing.
//
// still is the oracle. It replays a candidate from a clean emulator and says
// whether the same thing still goes wrong, so every reduction is kept only when
// it reproduces and the result is guaranteed to.
//
// Every accepted candidate is strictly smaller than what it replaces, fewer
// steps or fewer bytes in one step, so the fixpoint is always reached. For a
// deterministic still the result is deterministic too.

// Shrink returns the smallest script it could reach that still fails.
func Shrink(s Script, still func(Script) bool) Script {
	best := s
	for {
		steps, bytes := len(best), totalBytes(best)
		best = dropBlocks(best, still)
		best = dropSingles(best, still)
		best = simplify(best, still)
		// A round that only simplified can still have made a step removable,
		// so the loop ends on a round that changed nothing at all, not on one
		// that removed no step.
		if len(best) == steps && totalBytes(best) == bytes {
			return best
		}
	}
}

func totalBytes(s Script) int {
	n := 0
	for _, seq := range s {
		n += len(seq.Bytes)
	}
	return n
}

// dropBlocks is the delta-debugging half: remove contiguous runs, coarse first.
// A failure that needs a setup step and a trigger collapses fast this way,
// where removing one at a time stalls on the setup.
func dropBlocks(s Script, still func(Script) bool) Script {
	for n := len(s) / 2; n >= 1; n /= 2 {
		for i := 0; i+n <= len(s); {
			cand := without(s, i, i+n)
			if len(cand) > 0 && still(cand) {
				s = cand
				continue
			}
			i += n
		}
		if n == 1 {
			break
		}
	}
	return s
}

// dropSingles sweeps back to front so an accepted removal never invalidates an
// index still to be visited.
func dropSingles(s Script, still func(Script) bool) Script {
	for i := len(s) - 1; i >= 0; i-- {
		if i >= len(s) {
			continue
		}
		cand := without(s, i, i+1)
		if len(cand) > 0 && still(cand) {
			s = cand
		}
	}
	return s
}

// simplify makes the steps that remain plainer without making the script
// shorter. A repeated text run becomes one copy, an oversized payload becomes a
// small one, and a sequence carrying four parameters loses the ones the failure
// does not need. What this buys is a report that says which parameter mattered
// instead of leaving the reader to work it out.
//
// Each step is simplified until none of its simpler forms still fails, not just
// once: halving a payload once leaves most of it, and dropping one parameter
// leaves the rest.
func simplify(s Script, still func(Script) bool) Script {
	for i := range s {
		for changed := true; changed; {
			changed = false
			for _, cand := range simpler(s[i]) {
				trial := clone(s)
				trial[i] = cand
				if still(trial) {
					s = trial
					changed = true
					break
				}
			}
		}
	}
	return s
}

// simpler offers plainer versions of one step, most aggressive first, so the
// first one that still fails is also the smallest the step can become in one
// move. Every candidate is strictly shorter than the step, which is what makes
// simplify terminate.
func simpler(seq Seq) []Seq {
	// The description was written for the bytes the generator drew, and a
	// simplified step no longer carries them: "SU scroll up (params 63;24;3)"
	// beside the bytes of a bare SU contradicts itself. So a simplified step
	// keeps the original description, marked as describing what it was
	// reduced from, once, however many times it is simplified after that.
	desc := seq.Desc
	if !strings.Contains(desc, reducedMarker) {
		from := quote(seq.Bytes)
		if len(from) > 32 {
			from = strconv.Itoa(len(seq.Bytes)) + " bytes"
		}
		desc += reducedMarker + from + ")"
	}

	var out []Seq
	add := func(bytes string) {
		if len(bytes) < len(seq.Bytes) {
			out = append(out, Seq{Kind: seq.Kind, Bytes: bytes, Desc: desc, Cols: seq.Cols, Rows: seq.Rows})
		}
	}

	// A long run of the same payload almost never needs to be long.
	if n := len(seq.Bytes); n > 64 {
		add(seq.Bytes[:16])
		add(seq.Bytes[:n/2])
	}

	// A CSI with parameters usually turns on one of them, or on none.
	if strings.HasPrefix(seq.Bytes, "\x1b[") && len(seq.Bytes) > 3 {
		body := seq.Bytes[2:]
		last := len(body) - 1
		final := body[last:]
		add("\x1b[" + final)
		if params := body[:last]; strings.Contains(params, ";") {
			parts := strings.Split(params, ";")
			for drop := len(parts) - 1; drop >= 0; drop-- {
				kept := append(append([]string{}, parts[:drop]...), parts[drop+1:]...)
				add("\x1b[" + strings.Join(kept, ";") + final)
			}
		}
	}
	return out
}

// reducedMarker introduces the note a simplified step's description carries.
const reducedMarker = " (reduced from "

func without(s Script, i, j int) Script {
	out := make(Script, 0, len(s)-(j-i))
	out = append(out, s[:i]...)
	out = append(out, s[j:]...)
	return out
}

func clone(s Script) Script {
	out := make(Script, len(s))
	copy(out, s)
	return out
}
