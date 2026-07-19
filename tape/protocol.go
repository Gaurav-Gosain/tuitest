package tape

import "sort"

// Result reports how a Protocol matched the head of an input buffer.
type Result int

const (
	// NoMatch means the buffer does not begin with this protocol's syntax. The
	// decoder moves on to the next protocol.
	NoMatch Result = iota
	// Partial means the buffer begins with a proper prefix of this protocol's
	// syntax and more bytes could complete it. The decoder holds the bytes back
	// rather than guessing, which is what keeps a sequence split across two
	// reads from being decoded as two wrong things.
	Partial
	// Full means the protocol consumed a complete sequence.
	Full
)

// Fidelity states how exactly a protocol's Encode reproduces the bytes its
// Decode consumed. It documents, rather than weakens, the round-trip property:
// see the package tests for the two tiers.
type Fidelity int

const (
	// Exact means Encode(Decode(b)) == b for every b the protocol matches Full.
	Exact Fidelity = iota
	// Canonical means Encode(Decode(b)) may differ from b, but only by a
	// documented normalization, and Decode(Encode(Decode(b))) == Decode(b).
	Canonical
)

// Modes is the terminal mode context a protocol needs to decode unambiguously.
// It is observed from the child's *output* stream, which is what makes the
// otherwise ambiguous encodings decidable: SGR-pixel mouse reports are byte for
// byte identical to ordinary SGR ones and differ only in whether the program
// asked for mode 1016.
//
// The zero Modes reports every mode unset, which is the correct default: a
// program that never enabled a mode cannot be sending its reports.
//
// It is deliberately a lookup rather than a fixed set of fields or an
// enumerated list of interesting modes. A protocol added later can ask about
// any mode it likes without anything outside its own file changing, which is
// the extensibility property this package claims.
type Modes struct {
	lookup func(int) bool
}

// NewModes builds a Modes from a mode lookup, normally the live terminal's.
func NewModes(lookup func(int) bool) Modes { return Modes{lookup: lookup} }

// ModeSet builds a Modes from a fixed set of DEC private mode numbers.
func ModeSet(set map[int]bool) Modes {
	return Modes{lookup: func(n int) bool { return set[n] }}
}

// DEC reports whether the given DEC private mode is currently set. The zero
// Modes reports every mode unset.
func (m Modes) DEC(n int) bool {
	if m.lookup == nil {
		return false
	}
	return m.lookup(n)
}

// Protocol is one self-contained terminal input encoding: a decoder from bytes
// to tape commands, an encoder from commands back to bytes, and a name.
//
// Adding support for a new encoding means writing one file that implements this
// interface and calling Register from its init. Nothing in the recorder, the
// parser, the printer or the player needs to change, and no other protocol is
// affected. The mouse, paste and focus protocols in this package are each such
// a file, and the focus protocol was deliberately added last, through this seam
// only, to demonstrate that the seam is sufficient.
type Protocol interface {
	// Name identifies the protocol in diagnostics.
	Name() string

	// Priority breaks ties when two protocols both return Full for the same
	// number of bytes. Higher wins. Most protocols return 0, since a tie means
	// two encodings are genuinely indistinguishable and one must be preferred.
	Priority() int

	// Decode attempts to consume a sequence from the head of buf. It must not
	// look past the sequence it claims, and must return Partial rather than
	// NoMatch when buf is a proper prefix of a sequence it would match.
	Decode(buf []byte, m Modes) (n int, cmds []Command, r Result)

	// Encode renders a command this protocol owns back to the bytes a terminal
	// would have sent. It returns false for commands it does not own, and for
	// commands its wire format cannot represent, in which case the caller falls
	// back to a raw capture rather than emitting something inexact.
	Encode(c Command, m Modes) ([]byte, bool)

	// Fidelity states which round-trip tier this protocol satisfies.
	Fidelity() Fidelity
}

// registered holds the protocols in registration order, and ordered holds them
// sorted by priority. Sorting happens once per registration rather than once
// per decode, because decodeSequence runs for every escape byte of every
// recorded session.
//
// Keeping both is what makes the ordering independent of file naming:
// registration order breaks ties between equal priorities, and nothing else
// depends on it.
var (
	registered []Protocol
	ordered    []Protocol
)

// Register adds a protocol to the decoder and encoder. It is meant to be called
// from an init function. Registering the same name twice panics, because a
// silently shadowed protocol is a bug that would otherwise surface as
// mysteriously misdecoded input.
func Register(p Protocol) {
	for _, q := range registered {
		if q.Name() == p.Name() {
			panic("tape: protocol " + p.Name() + " registered twice")
		}
	}
	registered = append(registered, p)

	ordered = make([]Protocol, len(registered))
	copy(ordered, registered)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Priority() > ordered[j].Priority()
	})
}

// decodeSequence runs the registered protocols against the head of buf and
// picks the best match: the longest Full match, ties broken by priority and
// then registration order.
//
// It returns hold=true when no protocol matched Full but at least one reported
// Partial, meaning the buffer ends inside a sequence and the caller must wait
// for more bytes instead of committing to a reading of a truncated one.
func decodeSequence(buf []byte, m Modes) (n int, cmds []Command, ok, hold bool) {
	best := -1
	var bestCmds []Command
	partial := false

	for _, p := range ordered {
		got, produced, r := p.Decode(buf, m)
		switch r {
		case Partial:
			partial = true
		case Full:
			if got <= 0 {
				// A protocol that claims a match but consumes nothing would
				// spin the decode loop forever. Treat it as no match.
				continue
			}
			// A Full match must be re-encodable, or semantic decoding would
			// lose fidelity that the raw fallback would have preserved. A
			// protocol that cannot encode what it just decoded is declining the
			// match, and the sequence falls through to Raw.
			if !reencodes(p, buf[:got], produced, m) {
				continue
			}
			if got > best {
				best, bestCmds = got, produced
			}
		}
	}

	if best > 0 {
		return best, bestCmds, true, false
	}
	return 0, nil, false, partial
}

// reencodes reports whether p can render the commands it just produced back to
// the bytes it consumed. This is the invariant that lets semantic decoding be a
// pure readability win: anything a protocol cannot round-trip is left to Raw,
// which replays exactly.
//
// For a protocol declaring Exact fidelity the check is byte for byte, and it is
// what enforces the tier rather than merely documenting it. A protocol does not
// have to reject unusual spellings of its own syntax by hand: a mouse report
// written with a redundant leading zero re-encodes without it, fails this
// comparison, and is captured raw, so the recording still replays the bytes the
// terminal actually sent. Only a protocol that declares Canonical is allowed to
// come back with different bytes, and its normalization is documented with it.
func reencodes(p Protocol, src []byte, cmds []Command, m Modes) bool {
	var out []byte
	for _, c := range cmds {
		b, ok := p.Encode(c, m)
		if !ok {
			return false
		}
		out = append(out, b...)
	}
	if p.Fidelity() == Exact {
		return string(out) == string(src)
	}
	return true
}

// encodeCommand renders a command back to the bytes that would produce it,
// consulting the registered protocols. It is the encoding half of the
// round-trip property and the single place the player and the fuzz targets
// agree on what a command means on the wire.
func encodeCommand(c Command, m Modes) ([]byte, bool) {
	for _, p := range ordered {
		if b, ok := p.Encode(c, m); ok {
			return b, true
		}
	}
	return nil, false
}
