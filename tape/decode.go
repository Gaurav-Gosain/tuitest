package tape

import (
	"strings"
	"unicode/utf8"
)

// inputDecoder converts raw terminal input into tape commands. Printable runs
// coalesce into a single Type command and everything else is offered to the
// registered protocols, which turn it into named Key, Mouse, Paste, Focus and
// Reply commands.
//
// Nothing is ever dropped. A sequence no protocol claims is framed by its
// ECMA-48 shape and captured verbatim as a Raw command, so the recording
// replays byte for byte whether or not a decoder exists for it. Protocol
// coverage is a readability feature layered on that guarantee, which is why
// this type no longer counts dropped sequences: the count would always be zero.
//
// Decoding is chunk-oriented: a terminal delivers one keypress, or one burst of
// pasted text, per read. Only genuinely incomplete sequences are held back for
// the next chunk.
type inputDecoder struct {
	pending []byte   // incomplete sequence carried into the next chunk
	text    []byte   // printable run not yet flushed to a Type command
	keys    []string // consecutive Key tokens not yet flushed
	cmds    []Command

	// keyModes is the mode context the buffered key run was decoded under.
	// Keys only coalesce onto one line while this stays the same, so a line
	// never mixes keys that need different encodings to replay.
	keyModes Modes

	// modes is the terminal state observed on the child's output stream. The
	// same bytes mean different keys depending on what the program
	// negotiated, so the decoder is told rather than left to guess.
	modes Modes
}

// setModes updates the mode context used to interpret subsequent input.
func (d *inputDecoder) setModes(m Modes) { d.modes = m }

// feed decodes one chunk of raw input.
func (d *inputDecoder) feed(chunk []byte) {
	buf := chunk
	if len(d.pending) > 0 {
		buf = append(append([]byte(nil), d.pending...), chunk...)
		d.pending = nil
	}

	for i := 0; i < len(buf); {
		b := buf[i]

		// Printable ASCII accumulates into a Type run.
		if b > 0x20 && b < 0x7f || b == ' ' {
			d.text = append(d.text, b)
			i++
			continue
		}

		// Non-ASCII: a C1 control in 0x80-0x9f cannot start a UTF-8 rune,
		// so it is unambiguously a control introducer. Anything else is
		// text.
		if b >= 0x80 {
			if b <= 0x9f {
				n, hold := d.decodeControl(buf[i:])
				if hold {
					d.hold(buf[i:])
					return
				}
				i += n
				continue
			}
			if !utf8.FullRune(buf[i:]) {
				d.hold(buf[i:])
				return
			}
			_, size := utf8.DecodeRune(buf[i:])
			d.text = append(d.text, buf[i:i+size]...)
			i += size
			continue
		}

		// ESC or a C0 control: the protocols decide.
		n, hold := d.decodeControl(buf[i:])
		if hold {
			d.hold(buf[i:])
			return
		}
		i += n
	}
}

// maxPendingBytes bounds how much of an unterminated sequence the decoder will
// hold waiting for a final byte. A control sequence is a few dozen bytes even
// with generous parameters, so anything past this is a program that will never
// terminate it, or input that is not a control sequence at all. Without a bound
// the hold grows for as long as the bytes keep arriving.
const maxPendingBytes = 4096

// hold stashes an incomplete sequence for the next chunk to complete.
//
// The hold is bounded: past maxPendingBytes the sequence is never going to
// terminate, so waiting longer only grows the buffer. Giving up emits the bytes
// verbatim as Raw rather than discarding them, so the recording still replays
// the session exactly whether or not the bytes could be named.
func (d *inputDecoder) hold(rest []byte) {
	if len(rest) > maxPendingBytes {
		d.emitRaw(rest)
		d.pending = nil
		return
	}
	d.pending = append([]byte(nil), rest...)
}

// decodeControl decodes one control sequence at the head of rest. It returns
// how many bytes were consumed, or hold=true when more input is needed.
//
// The order is deliberate: registered protocols first so a recognized sequence
// gets a readable name, then the framer so an unrecognized one is still
// captured whole, and only then a single raw byte so no input can ever be
// skipped. The last case cannot lose data because it always advances.
func (d *inputDecoder) decodeControl(rest []byte) (n int, hold bool) {
	switch n, cmds, r := dispatch(rest, d.modes); r {
	case Full:
		d.absorb(cmds)
		return n, false
	case Partial:
		return 0, true
	}

	// No protocol claimed it. Frame the sequence by its shape and keep the
	// bytes verbatim, which is what makes coverage optional.
	if n, complete, ok := frameEnd(rest); ok {
		if !complete {
			return 0, true
		}
		if n > 0 {
			d.emitRaw(rest[:n])
			return n, false
		}
	}

	// Not a control sequence and no protocol wants it: one byte, verbatim.
	d.emitRaw(rest[:1])
	return 1, false
}

// absorb appends decoded commands, merging consecutive single-token Key
// commands into one Key line so a run of keystrokes reads as "Key Enter Tab"
// rather than one line each.
func (d *inputDecoder) absorb(cmds []Command) {
	for _, c := range cmds {
		if c.Kind == KindKey && len(c.Keys) == 1 && !c.KeyAttrs.set() {
			d.emitKeyModes(c.Keys[0], mergeModes(d.modes, c.Modes))
			continue
		}
		d.flush()
		// A protocol may report modes it inferred from the sequence
		// itself, which is stronger evidence than the ambient context: a
		// kitty key report proves kitty was negotiated even if the mode
		// mirror missed the enabling sequence. Keep what it inferred.
		c.Modes = mergeModes(d.modes, c.Modes)
		d.cmds = append(d.cmds, c)
	}
}

// emitRaw records bytes with no semantic decoding. Raw replays byte for byte,
// so this is always correct, merely less readable than a named command.
func (d *inputDecoder) emitRaw(b []byte) {
	d.flush()
	d.cmds = append(d.cmds, Command{Kind: KindRaw, Text: string(b), Modes: d.modes})
}

// emitKey appends a key token decoded under the ambient modes.
func (d *inputDecoder) emitKey(tok string) { d.emitKeyModes(tok, d.modes) }

// emitKeyModes appends a key token, first flushing any pending printable run so
// the commands stay in input order, and flushing the key run when the mode
// context changes so one Key line always replays under one set of modes.
func (d *inputDecoder) emitKeyModes(tok string, m Modes) {
	d.flushText()
	if len(d.keys) > 0 && !d.keyModes.sameEncoding(m) {
		d.flushKeys()
	}
	d.keyModes = m
	d.keys = append(d.keys, tok)
}

// flushText turns the accumulated printable run into a Type command. Leading and
// trailing spaces become Key Space tokens instead, because a tape line cannot
// carry them unambiguously through an editor.
func (d *inputDecoder) flushText() {
	if len(d.text) == 0 {
		return
	}
	s := string(d.text)
	d.text = d.text[:0]

	lead := len(s) - len(strings.TrimLeft(s, " "))
	for range lead {
		d.keys = append(d.keys, "Space")
	}
	s = s[lead:]
	if s == "" {
		return
	}
	trail := len(s) - len(strings.TrimRight(s, " "))
	body := s[:len(s)-trail]

	if body != "" {
		d.flushKeys()
		d.cmds = append(d.cmds, Command{Kind: KindType, Text: body, Modes: d.modes})
	}
	for range trail {
		d.keys = append(d.keys, "Space")
	}
}

// flushKeys turns pending key tokens into a single Key command.
func (d *inputDecoder) flushKeys() {
	if len(d.keys) == 0 {
		return
	}
	d.cmds = append(d.cmds, Command{Kind: KindKey, Keys: d.keys, Modes: d.keyModes})
	d.keys = nil
	d.keyModes = d.modes
}

// close flushes everything still buffered. A held-back sequence at end of
// stream is as complete as it will ever get, so it is emitted as Raw rather
// than guessed at or discarded.
func (d *inputDecoder) close() {
	if len(d.pending) > 0 {
		stuck := d.pending
		d.pending = nil
		if len(stuck) == 1 && stuck[0] == 0x1b {
			d.emitKey("Esc")
		} else {
			d.emitRaw(stuck)
		}
	}
	d.flush()
}

// flush finalizes the printable run and key tokens still in progress. It is
// called at the points where the tape needs a stable prefix, not after every
// chunk: a terminal delivers one read per keystroke, so flushing per chunk would
// turn a typed word into one Type command per character.
func (d *inputDecoder) flush() {
	d.resolvePendingEsc()
	d.flushText()
	d.flushKeys()
}

// resolvePendingEsc settles a held lone ESC as the Esc key. feed cannot make
// that call, because at a read boundary the byte is equally the Esc key and the
// start of an arrow key; a flush point can, because the burst is over there and
// no bytes are coming to complete a longer sequence.
func (d *inputDecoder) resolvePendingEsc() {
	if len(d.pending) == 1 && d.pending[0] == 0x1b {
		d.pending = nil
		d.emitKey("Esc")
	}
}

// take returns the commands completed so far and clears them, leaving any
// in-progress text or key run buffered for the next chunk to continue.
func (d *inputDecoder) take() []Command {
	out := d.cmds
	d.cmds = nil
	return out
}
