package tape

import (
	"sort"
	"strings"
)

// Result reports how much of the buffer a protocol recognized.
type Result int

const (
	// NoMatch means the bytes are not this protocol's, and the dispatcher
	// should try another. A protocol must also return NoMatch when it
	// recognizes a sequence it cannot faithfully re-encode, so the raw
	// fallback keeps the recording lossless.
	NoMatch Result = iota
	// Partial means the bytes are a proper prefix of this protocol's
	// sequence and more input could complete it.
	Partial
	// Full means n bytes were consumed and cmds represents them.
	Full
)

// Fidelity states how exactly a protocol re-encodes what it decoded.
type Fidelity int

const (
	// Exact means Encode(Decode(b)) reproduces b byte for byte.
	Exact Fidelity = iota
	// Canonical means Encode may emit a different but behaviourally
	// identical spelling, so only Decode(Encode(Decode(b))) == Decode(b)
	// holds. Legacy keys, modifyOtherKeys and kitty are Canonical because
	// the wire format has redundant spellings; see docs/input-protocols.md
	// for the exact normalization list.
	Canonical
)

// Modes is the terminal state that decides what a given byte sequence means.
// The same bytes are a different key depending on what the program under test
// negotiated, so the decoder cannot be a pure function of the input stream: it
// has to be told the modes observed on the child's output stream.
//
// Named fields carry the negotiated keyboard state that is not a DEC private
// mode, such as the kitty flag stack. The lookup carries everything that is
// one, so a protocol added later can ask about any mode it likes without this
// struct growing a field for it. That is what keeps the extensibility claim
// true for modes as well as for sequences.
type Modes struct {
	// AppCursor is DECCKM (mode 1). Cursor keys send SS3 rather than CSI.
	AppCursor bool
	// AppKeypad is DECKPAM. The numeric keypad sends SS3 forms.
	AppKeypad bool
	// KittyFlags is the top of the kitty keyboard progressive-enhancement
	// flag stack, as set by CSI > flags u / CSI = flags ; mode u.
	KittyFlags int
	// ModifyOtherKeys is the xterm modifyOtherKeys level, 0 when off.
	ModifyOtherKeys int
	// BracketedPaste is DEC private mode 2004.
	BracketedPaste bool

	// lookup answers for any DEC private mode, normally by reading the live
	// terminal. It is shared rather than copied, so a Modes is cheap to pass
	// by value and protocols must treat it as read only.
	lookup func(int) bool
}

// NewModes builds a Modes from a DEC private mode lookup, normally the live
// terminal's. The named fields that shadow a DEC mode are filled from the same
// lookup, so a protocol may read either without them disagreeing.
func NewModes(lookup func(int) bool) Modes {
	m := Modes{lookup: lookup}
	if lookup != nil {
		m.AppCursor = lookup(1)
		m.BracketedPaste = lookup(2004)
	}
	return m
}

// ModeSet builds a Modes from a fixed set of DEC private mode numbers. It is
// the form tests use, where there is no live terminal to ask.
func ModeSet(set map[int]bool) Modes {
	return NewModes(func(n int) bool { return set[n] })
}

// DEC reports whether the given DEC private mode is set. The zero Modes reports
// every mode unset, which is the state of a terminal that has just started: no
// mode is on until the program turns it on.
func (m Modes) DEC(n int) bool {
	switch n {
	case 1:
		if m.AppCursor {
			return true
		}
	case 2004:
		if m.BracketedPaste {
			return true
		}
	}
	if m.lookup == nil {
		return false
	}
	return m.lookup(n)
}

// sameEncoding reports whether two mode contexts would encode a key the same
// way. Only the negotiated state the encoders read is compared; the DEC lookup
// is a live view of the terminal and is not comparable, nor should it be, since
// two keys recorded under the same negotiation belong on one line however many
// unrelated modes changed between them.
func (m Modes) sameEncoding(o Modes) bool {
	return m.AppCursor == o.AppCursor &&
		m.AppKeypad == o.AppKeypad &&
		m.KittyFlags == o.KittyFlags &&
		m.ModifyOtherKeys == o.ModifyOtherKeys &&
		m.BracketedPaste == o.BracketedPaste
}

// Protocol decodes one input protocol into tape commands and encodes those
// commands back into bytes. Adding support for a protocol means adding one file
// with a Protocol implementation and its tests, then calling Register; no other
// file changes. That is the whole extensibility contract.
type Protocol interface {
	// Name identifies the protocol in diagnostics and must be unique.
	Name() string

	// Priority breaks ties when two protocols claim the same number of
	// bytes. Higher wins. Equal priorities fall back to registration order.
	Priority() int

	// Decode attempts to consume a sequence at the head of buf. It must not
	// modify buf. Returning Full with n <= 0 is a programming error and is
	// treated as NoMatch.
	Decode(buf []byte, m Modes) (n int, cmds []Command, r Result)

	// Encode renders a command this protocol owns back into bytes. The
	// second return is false when the command is not this protocol's.
	Encode(c Command, m Modes) ([]byte, bool)

	// Fidelity states whether Encode reproduces bytes exactly.
	Fidelity() Fidelity

	// Keyboard reports whether this protocol decodes keyboard input, and so
	// whether it is allowed to emit KindKey and KindType. A protocol that
	// decodes terminal replies must return false: reading a reply as
	// keystrokes corrupts a recording silently, which is the failure this
	// rule exists to make structurally impossible rather than merely
	// discouraged.
	Keyboard() bool
}

// registry holds the registered protocols in registration order.
var registry []Protocol

// Register adds a protocol to the decoder. It is the only wiring a new protocol
// needs. Registering a duplicate name panics, because two protocols answering
// to the same name is always a build mistake rather than a runtime condition.
func Register(p Protocol) {
	for _, existing := range registry {
		if existing.Name() == p.Name() {
			panic("tape: duplicate protocol " + p.Name())
		}
	}
	registry = append(registry, p)
}

// Protocols returns the registered protocols in dispatch order, highest
// priority first. It exists so tests can enumerate what is installed without
// reaching into package state.
func Protocols() []Protocol {
	out := append([]Protocol(nil), registry...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Priority() > out[j].Priority()
	})
	return out
}

// dispatch decodes the sequence at the head of buf using the registered
// protocols. It returns the winning match, or NoMatch when no protocol claimed
// the bytes and the caller should fall back to the framer.
//
// The rule is longest Full match wins, ties on Priority then registration
// order. A Partial with no Full means the sequence may still be completed by
// more input, so the caller must hold rather than guess.
//
// Every Full match is verified re-encodable before it is accepted. A protocol
// that decodes a sequence it cannot reproduce is downgraded to NoMatch and the
// bytes become Raw. That is what makes semantic decoding a pure readability
// layer: it can never cost fidelity, only add names.
func dispatch(buf []byte, m Modes) (n int, cmds []Command, r Result) {
	var (
		best     []Command
		bestN    int
		bestPrio int
		found    bool
		partial  bool
	)

	for _, p := range registry {
		pn, pcmds, pr := p.Decode(buf, m)
		switch pr {
		case Partial:
			partial = true
		case Full:
			if pn <= 0 || pn > len(buf) {
				continue
			}
			if !validDecode(p, pcmds) {
				continue
			}
			if !reencodes(p, pcmds, buf[:pn], m) {
				continue
			}
			if !representable(pcmds) {
				continue
			}
			if !found || pn > bestN || (pn == bestN && p.Priority() > bestPrio) {
				best, bestN, bestPrio, found = pcmds, pn, p.Priority(), true
			}
		}
	}

	if found {
		return bestN, best, Full
	}
	if partial {
		return 0, nil, Partial
	}
	return 0, nil, NoMatch
}

// validDecode enforces the rule that only a keyboard protocol may produce
// keyboard commands. This is the invariant behind the reported corruption: an
// APC reply read as Alt+_ , typed text and Alt+\ replays as nonsense, and
// unlike a dropped sequence it does so silently. A protocol that breaks the
// rule is ignored and its bytes fall through to Raw, which replays them
// correctly if unreadably.
func validDecode(p Protocol, cmds []Command) bool {
	if p.Keyboard() {
		return true
	}
	for _, c := range cmds {
		if c.Kind == KindKey || c.Kind == KindType {
			return false
		}
	}
	return true
}

// representable reports whether commands survive being written to a tape and
// read back. A recording is a file, so a decode that cannot be written down is
// not a decode: a key token containing whitespace resolves perfectly in memory
// and then fails to parse on the next run.
//
// Checking this at the registry rather than in each protocol means a protocol
// cannot introduce the failure at all. The bytes fall through to Raw, which is
// always representable because it is quoted.
func representable(cmds []Command) bool {
	back, err := Parse(strings.NewReader(Sprint(cmds)))
	if err != nil {
		return false
	}
	return commandsEquivalent(cmds, back)
}

// reencodes checks that a candidate decode survives the round trip its
// protocol promises. An Exact protocol must reproduce the consumed bytes; a
// Canonical one need only reproduce bytes that decode to the same commands.
//
// This is the invariant that lets the raw fallback backstop every semantic
// decoder rather than only the ones nobody has written yet.
func reencodes(p Protocol, cmds []Command, consumed []byte, m Modes) bool {
	enc, ok := encodeWith(p, cmds, m)
	if !ok {
		return false
	}
	if string(enc) == string(consumed) {
		return true
	}
	if p.Fidelity() == Exact {
		return false
	}
	// Canonical: the re-encoded bytes must decode to the same commands.
	rn, again, rr := p.Decode(enc, m)
	if rr != Full || rn != len(enc) {
		return false
	}
	return commandsEqual(cmds, again)
}

// encodeWith renders a whole command slice through one protocol.
func encodeWith(p Protocol, cmds []Command, m Modes) ([]byte, bool) {
	var out []byte
	for _, c := range cmds {
		b, ok := p.Encode(c, m)
		if !ok {
			return nil, false
		}
		out = append(out, b...)
	}
	return out, true
}

// encodeCommands renders decoded commands back to the bytes that produced
// them, which is the byte direction of the round-trip property. Raw and Paste
// carry their own bytes; everything else is offered to each protocol in
// dispatch order until one claims it.
func encodeCommands(cmds []Command) ([]byte, error) {
	var out []byte
	ordered := Protocols()
	for _, c := range cmds {
		b, err := encodeCommand(c, ordered)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

func encodeCommand(c Command, ordered []Protocol) ([]byte, error) {
	switch c.Kind {
	case KindRaw:
		return []byte(c.Text), nil
	case KindType:
		return []byte(c.Text), nil
	}
	m := c.Modes
	for _, p := range ordered {
		if b, ok := p.Encode(c, m); ok {
			return b, nil
		}
	}

	// Nothing claimed the token under the modes in force. Some chords exist
	// only under a negotiated protocol: Ctrl+Enter is what modifyOtherKeys was
	// invented to express, and the legacy encoding folds it onto plain Enter.
	// A tape file carries no mode context, so resolving such a token strictly
	// under default modes would fail, and the recorder would be forced to write
	// those keys as Raw rather than by name.
	//
	// So the token is offered again with every protocol's own negotiation
	// assumed. This is a fallback rather than the first choice, which is what
	// keeps an ordinary key in its ordinary spelling: legacy claims "a" on the
	// first pass and the question never arises. A token only reaches here when
	// no unnegotiated encoding of it exists at all, and in that case the bytes
	// of the protocol that invented the chord are the only faithful answer.
	for _, p := range ordered {
		if b, ok := p.Encode(c, permissiveModes); ok {
			return b, nil
		}
	}
	return nil, &unencodableError{cmd: c}
}

// permissiveModes assumes every negotiable input protocol is active. It is used
// only as the second pass of encodeCommand, to resolve chords that have no
// spelling outside a negotiated protocol.
var permissiveModes = Modes{
	KittyFlags:      1,
	ModifyOtherKeys: 2,
}

// wireBytes renders one command to the bytes a terminal would send for it,
// consulting the registered protocols in dispatch order. It is what the player
// uses for commands it has no direct terminal call for, such as Focus, so the
// bytes replayed are the bytes some protocol claims to own rather than a
// spelling written out a second time in the player.
func wireBytes(c Command, m Modes) ([]byte, bool) {
	for _, p := range Protocols() {
		if b, ok := p.Encode(c, m); ok {
			return b, true
		}
	}
	return nil, false
}

type unencodableError struct{ cmd Command }

func (e *unencodableError) Error() string {
	return "tape: no protocol can encode " + e.cmd.Kind.String()
}

// commandsEqual compares decoded commands for the round-trip check. It ignores
// Line, which is a source position rather than content, and compares only the
// fields the input decoders populate.
func commandsEqual(a, b []Command) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Kind != y.Kind || x.Text != y.Text || x.KeyAttrs != y.KeyAttrs {
			return false
		}
		if len(x.Keys) != len(y.Keys) {
			return false
		}
		for j := range x.Keys {
			if x.Keys[j] != y.Keys[j] {
				return false
			}
		}
		if x.Mouse != y.Mouse {
			return false
		}
	}
	return true
}

// mergeModes combines the ambient mode context with whatever a protocol
// inferred from the sequence it just decoded. Inference wins, because a
// sequence that only exists under a negotiated mode is direct evidence that the
// mode is on, whereas the ambient context can lag if the enabling sequence was
// missed.
func mergeModes(ambient, inferred Modes) Modes {
	out := ambient
	if inferred.KittyFlags > out.KittyFlags {
		out.KittyFlags = inferred.KittyFlags
	}
	if inferred.ModifyOtherKeys > out.ModifyOtherKeys {
		out.ModifyOtherKeys = inferred.ModifyOtherKeys
	}
	out.AppCursor = out.AppCursor || inferred.AppCursor
	out.AppKeypad = out.AppKeypad || inferred.AppKeypad
	out.BracketedPaste = out.BracketedPaste || inferred.BracketedPaste
	return out
}

// commandsEquivalent compares two command slices for the round-trip property,
// treating them as equal when they drive the program identically.
//
// It differs from commandsEqual in exactly one respect: how consecutive keys are
// grouped onto Key lines is not significant. "Key d" followed by "Key Ctrl+o"
// and the single line "Key d Ctrl+o" send the same bytes in the same order, and
// the recorder may group them differently depending on the mode context each key
// was decoded under. Grouping is a readability choice, so requiring it to be
// stable would be asserting something the format does not promise.
//
// Everything else is compared strictly, including key order, attributes and the
// payload of Raw and Type commands.
func commandsEquivalent(a, b []Command) bool {
	return commandsEqual(flattenKeys(a), flattenKeys(b))
}

// flattenKeys merges adjacent attribute-free Key commands into one, which is the
// normal form grouping differences collapse to.
func flattenKeys(cmds []Command) []Command {
	out := make([]Command, 0, len(cmds))
	for _, c := range cmds {
		last := len(out) - 1
		if c.Kind == KindKey && !c.KeyAttrs.set() &&
			last >= 0 && out[last].Kind == KindKey && !out[last].KeyAttrs.set() {
			out[last].Keys = append(append([]string(nil), out[last].Keys...), c.Keys...)
			continue
		}
		out = append(out, c)
	}
	return out
}
