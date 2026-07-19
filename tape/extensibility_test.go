package tape

import (
	"strings"
	"testing"
)

// This file is the executable form of the extensibility claim: adding a
// protocol is one self-contained unit plus a Register call, with no changes to
// the recorder, the parser, the player or any other protocol.
//
// Everything below is confined to this file. If the claim were false, wiring a
// new protocol would require touching something else, and this test could not
// compile without that edit.

func init() { Register(exampleProtocol{}) }

// exampleProtocol decodes a made-up status report, CSI <n> W, purely to prove
// the seam. It is Exact, so the dispatcher holds it to byte-for-byte
// re-encoding.
type exampleProtocol struct{}

func (exampleProtocol) Name() string       { return "example-status" }
func (exampleProtocol) Priority() int      { return 5 }
func (exampleProtocol) Fidelity() Fidelity { return Exact }

func (exampleProtocol) Decode(buf []byte, m Modes) (int, []Command, Result) {
	body, final, n, r := csiFrame(buf)
	if r != Full {
		return 0, nil, r
	}
	if final != 'W' || len(body) == 0 {
		return 0, nil, NoMatch
	}
	for _, b := range body {
		if b < '0' || b > '9' {
			return 0, nil, NoMatch
		}
	}
	// Reported as Raw with a recognizable payload; a real protocol would
	// use its own command kind.
	return n, []Command{{Kind: KindRaw, Text: string(buf[:n])}}, Full
}

func (exampleProtocol) Encode(c Command, m Modes) ([]byte, bool) {
	if c.Kind != KindRaw || !strings.HasSuffix(c.Text, "W") {
		return nil, false
	}
	return []byte(c.Text), true
}

// TestRegisteredProtocolIsUsed checks that registering is the whole wiring: the
// dispatcher picks the protocol up with no other change anywhere.
func TestRegisteredProtocolIsUsed(t *testing.T) {
	var found bool
	for _, p := range Protocols() {
		if p.Name() == "example-status" {
			found = true
		}
	}
	if !found {
		t.Fatal("a registered protocol is not in the dispatch order")
	}

	const in = "\x1b[42W"
	n, cmds, r := dispatch([]byte(in), Modes{})
	if r != Full {
		t.Fatalf("dispatch result = %v, want Full", r)
	}
	if n != len(in) {
		t.Fatalf("consumed %d bytes, want %d", n, len(in))
	}
	if len(cmds) != 1 || cmds[0].Text != in {
		t.Fatalf("decoded %v, want the sequence verbatim", cmds)
	}
}

// TestUnknownProtocolStillRoundTrips is the other half of the claim: a sequence
// no protocol handles is not a problem to be fixed later. It already replays
// correctly, which is what makes protocol support optional.
func TestUnknownProtocolStillRoundTrips(t *testing.T) {
	// A CSI with a final byte nothing claims, and a control string from a
	// class that has no decoder at all.
	for _, in := range []string{"\x1b[42Y", "\x1b_Ginvented;payload\x1b\\"} {
		cmds := decodeCommands([]byte(in))
		for _, c := range cmds {
			if c.Kind == KindKey || c.Kind == KindType {
				t.Fatalf("%q decoded as keyboard input: %s",
					in, strings.TrimRight(Sprint(cmds), "\n"))
			}
		}
		got, err := encodeCommands(cmds)
		if err != nil {
			t.Fatalf("%q: re-encode: %v", in, err)
		}
		if string(got) != in {
			t.Fatalf("%q did not survive: got %q", in, got)
		}
	}
}
