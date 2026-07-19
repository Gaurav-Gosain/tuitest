package tape

import (
	"strconv"
	"strings"
)

// csiParams is a CSI parameter list with its sub-parameters: parameters are
// separated by ';' and sub-parameters within one by ':'. The kitty keyboard
// protocol needs sub-parameters to carry the shifted and base layouts of a key
// and the press/repeat/release event, so the legacy flat parse is not enough.
type csiParams [][]int

// parseCSIParams parses a parameter string. It returns ok=false for anything
// that is not a well-formed numeric parameter list, which is how a protocol
// declines a sequence that merely looks like its own.
//
// An omitted parameter or sub-parameter is recorded as -1 rather than 0, so a
// decoder can tell "not supplied" from an explicit zero. That distinction
// matters: in kitty a missing text parameter means no text, while an explicit
// zero would be a NUL codepoint.
func parseCSIParams(s string) (csiParams, bool) {
	if s == "" {
		return nil, true
	}
	groups := strings.Split(s, ";")
	out := make(csiParams, len(groups))
	for i, g := range groups {
		subs := strings.Split(g, ":")
		vals := make([]int, len(subs))
		for j, sub := range subs {
			if sub == "" {
				vals[j] = -1
				continue
			}
			v, err := strconv.Atoi(sub)
			if err != nil || v < 0 {
				return nil, false
			}
			vals[j] = v
		}
		out[i] = vals
	}
	return out, true
}

// at returns sub-parameter j of parameter i, or def when it is absent.
func (p csiParams) at(i, j, def int) int {
	if i >= len(p) || j >= len(p[i]) || p[i][j] < 0 {
		return def
	}
	return p[i][j]
}

// has reports whether sub-parameter j of parameter i was explicitly supplied.
func (p csiParams) has(i, j int) bool {
	return i < len(p) && j < len(p[i]) && p[i][j] >= 0
}

// group returns parameter i's sub-parameters, or nil.
func (p csiParams) group(i int) []int {
	if i >= len(p) {
		return nil
	}
	return p[i]
}

// csiFrame frames a CSI sequence and splits it into its parameter body and
// final byte, which is what every CSI protocol needs before it can decide
// whether the sequence is its own.
//
// It exists so the three keyboard protocols cannot each get the edge cases
// wrong separately. In particular a CSI aborted by an illegal byte is framed
// for the raw fallback but has no final byte at all, so treating the last byte
// as final would read outside the parameter area.
func csiFrame(buf []byte) (body []byte, final byte, n int, r Result) {
	if len(buf) < 2 || buf[0] != 0x1b || buf[1] != '[' {
		return nil, 0, 0, NoMatch
	}
	n, complete, ok := frameEnd(buf)
	if !ok {
		return nil, 0, 0, NoMatch
	}
	if !complete {
		return nil, 0, 0, Partial
	}
	// A well-formed CSI is at least ESC [ plus a final byte. Anything
	// shorter was aborted and belongs to the raw fallback.
	if n < 3 {
		return nil, 0, 0, NoMatch
	}
	final = buf[n-1]
	if final < 0x40 || final > 0x7e {
		return nil, 0, 0, NoMatch
	}
	return buf[2 : n-1], final, n, Full
}
