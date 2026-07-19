package tape

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// escSeq maps the byte sequence a terminal sends to the canonical tape token for
// it. It is the inverse of the named-key table in keys.go; where several tape
// names share a sequence (Return/Enter, Escape/Esc) the canonical one is chosen
// here so recordings are stable.
var escSeq = map[string]string{
	"\x1b[A":   "Up",
	"\x1b[B":   "Down",
	"\x1b[C":   "Right",
	"\x1b[D":   "Left",
	"\x1b[H":   "Home",
	"\x1b[F":   "End",
	"\x1b[2~":  "Insert",
	"\x1b[3~":  "Delete",
	"\x1b[5~":  "PageUp",
	"\x1b[6~":  "PageDown",
	"\x1bOP":   "F1",
	"\x1bOQ":   "F2",
	"\x1bOR":   "F3",
	"\x1bOS":   "F4",
	"\x1b[15~": "F5",
	"\x1b[17~": "F6",
	"\x1b[18~": "F7",
	"\x1b[19~": "F8",
	"\x1b[20~": "F9",
	"\x1b[21~": "F10",
	"\x1b[23~": "F11",
	"\x1b[24~": "F12",
}

// escSeqByLen holds the keys of escSeq longest first, so matching prefers
// "\x1b[15~" (F5) over any shorter sequence that shares its opening bytes.
var escSeqByLen = func() []string {
	out := make([]string, 0, len(escSeq))
	for k := range escSeq {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}()

// inputDecoder converts raw terminal input into tape commands. Printable runs
// coalesce into a single Type command and everything else becomes Key tokens,
// which is what makes a recording readable rather than a keystroke dump.
//
// Decoding is chunk-oriented: a terminal delivers one keypress, or one burst of
// pasted text, per read. Within a chunk an ESC followed by a printable rune is
// the meta encoding of Alt, whereas a chunk that ends on a bare ESC is the Esc
// key. Only genuinely incomplete sequences (a chunk ending inside a CSI or SS3
// introducer, or mid-rune) are held back for the next chunk.
type inputDecoder struct {
	pending []byte   // incomplete sequence carried into the next chunk
	text    []byte   // printable run not yet flushed to a Type command
	keys    []string // consecutive Key tokens not yet flushed
	cmds    []Command

	// modes is the terminal mode context observed from the child's output. It
	// is what makes otherwise ambiguous encodings decidable, such as an
	// SGR-pixel mouse report, which is byte for byte an ordinary SGR one.
	modes Modes
}

// feed decodes one chunk of raw input.
func (d *inputDecoder) feed(chunk []byte) {
	buf := chunk
	if len(d.pending) > 0 {
		buf = append(append([]byte(nil), d.pending...), chunk...)
		d.pending = nil
	}

	for i := 0; i < len(buf); {
		b := buf[i]
		switch {
		case b == 0x1b:
			n, hold := d.decodeEscape(buf[i:])
			if hold {
				d.pending = append([]byte(nil), buf[i:]...)
				return
			}
			i += n

		case b == '\t':
			d.emitKey("Tab")
			i++

		case b == '\r':
			d.emitKey("Enter")
			i++

		case b == 0x7f:
			d.emitKey("Backspace")
			i++

		case b < 0x20:
			d.emitKey(ctrlToken(b))
			i++

		default:
			if b >= utf8.RuneSelf && !utf8.FullRune(buf[i:]) {
				// Truncated multi-byte rune; wait for the rest.
				d.pending = append([]byte(nil), buf[i:]...)
				return
			}
			_, size := utf8.DecodeRune(buf[i:])
			d.text = append(d.text, buf[i:i+size]...)
			i += size
		}
	}
}

// decodeEscape handles a run starting with ESC. It returns how many bytes were
// consumed, or hold=true when the chunk ended inside an incomplete sequence.
//
// The order matters and is the whole design. Named keys come first, because
// they are the most specific and most readable reading. Then the registered
// protocols, each of which recognises a sequence class the tape has a first
// class representation for. Then, and this is the part that makes a recording
// trustworthy regardless of how many protocols exist, the framer: it finds the
// end of any escape sequence at all without knowing what it means, and the
// bytes are captured verbatim as Raw.
//
// Nothing reaches the end of this function without being represented. There is
// no drop path, which is why the recorder no longer has a dropped counter to
// report.
func (d *inputDecoder) decodeEscape(rest []byte) (n int, hold bool) {
	// A named key, but only when the buffer definitely holds all of it.
	for _, seq := range escSeqByLen {
		if strings.HasPrefix(string(rest), seq) {
			d.emitKey(escSeq[seq])
			return len(seq), false
		}
	}

	// A registered protocol: mouse, paste, focus, and whatever is added later.
	if n, cmds, ok, holdProto := decodeSequence(rest, d.modes); ok {
		d.flush()
		d.cmds = append(d.cmds, cmds...)
		return n, false
	} else if holdProto {
		return 0, true
	}

	// ESC + printable rune within one chunk is the Alt (meta) encoding. This is
	// only reached for an ESC that does not introduce a sequence at all, which
	// is why it no longer swallows OSC, DCS and APC introducers the way it once
	// turned an APC reply into Alt+underscore and typed text.
	if len(rest) >= 2 && isMetaPrefix(rest[1]) {
		if rest[1] >= utf8.RuneSelf && !utf8.FullRune(rest[1:]) {
			return 0, true
		}
		_, size := utf8.DecodeRune(rest[1:])
		if tok, ok := metaToken(rest[:1+size]); ok {
			d.emitKey(tok)
			return 1 + size, false
		}
		// The key has no chord spelling that reads back as itself, so it is
		// captured raw instead. See metaToken.
		d.emitRaw(rest[:1+size])
		return 1 + size, false
	}

	// Anything else that starts a sequence: frame it and keep the bytes.
	switch size, r := frame(rest); r {
	case frameIncomplete:
		return 0, true
	case frameComplete:
		if size == 1 {
			d.emitKey("Esc")
			return 1, false
		}
		d.emitRaw(rest[:size])
		return size, false
	}

	d.emitKey("Esc")
	return 1, false
}

// isMetaPrefix reports whether a byte following ESC should be read as the meta
// encoding of a key rather than as the introducer of a control sequence.
//
// The control-string introducers are excluded by name. Reading ESC _ as
// Alt+underscore is exactly the bug that turned a kitty graphics capability
// reply into three bogus keystrokes, so the exclusion is the fix.
func isMetaPrefix(b byte) bool {
	switch b {
	case '[', ']', 'P', '_', '^', 'X', 'O', 'N':
		return false
	}
	return b >= 0x20 && b != 0x7f
}

// metaToken spells the meta-encoded key in seq, which is ESC followed by one
// rune, as an Alt chord. It reports false when that spelling would not read
// back as exactly the bytes in seq.
//
// The check is not decoration, and it is deliberately made against the original
// bytes rather than against the decoded rune. Two distinct things go wrong
// otherwise:
//
//   - '+' is the chord grammar's own separator, so "Alt++" parses as an Alt
//     chord over an empty base and does not resolve at all. A recording
//     containing it replays as an error rather than as the key that was
//     pressed.
//   - a byte that is not valid UTF-8 decodes to the replacement rune, whose
//     spelling is three bytes. Comparing against the decoded rune would find
//     those three bytes equal to themselves and happily record a keystroke the
//     user never made, in place of the byte they did send.
//
// Rather than special-case either, the token is resolved and compared against
// its source bytes, which also holds for whatever is added to the key grammar
// later. Anything that fails falls back to a raw capture and still replays
// exactly.
func metaToken(seq []byte) (string, bool) {
	r, _ := utf8.DecodeRune(seq[1:])
	tok := "Alt+" + string(r)
	// A Key line is whitespace delimited, so a token containing whitespace
	// would be read back as a different, shorter token. Alt+Space is the real
	// case: it resolves perfectly well in memory and only falls apart once the
	// tape is written out and parsed again.
	if len(strings.Fields(tok)) != 1 || strings.Fields(tok)[0] != tok {
		return "", false
	}
	k, err := ResolveKey(tok)
	if err != nil || string(k) != string(seq) {
		return "", false
	}
	return tok, true
}

// emitRaw records bytes that no protocol claimed, verbatim. Replaying a Raw
// command writes exactly these bytes back to the child, so an unrecognised
// sequence still reproduces the session precisely; recognising it later only
// makes the tape easier to read.
func (d *inputDecoder) emitRaw(b []byte) {
	d.flush()
	d.cmds = append(d.cmds, Command{Kind: KindRaw, Text: string(b)})
}

// ctrlToken names a C0 control byte as a tape Ctrl chord. The mapping is the
// exact inverse of tuitest.Ctrl, so a decoded token replays byte-for-byte.
func ctrlToken(b byte) string {
	if b == 0 {
		return "Ctrl+@"
	}
	if b >= 1 && b <= 26 {
		return "Ctrl+" + string(rune('a'+b-1))
	}
	// 0x1c-0x1f are Ctrl with the punctuation whose low five bits match.
	return "Ctrl+" + string(rune(b|0x40))
}

// emitKey appends a key token, first flushing any pending printable run so the
// commands stay in input order.
func (d *inputDecoder) emitKey(tok string) {
	d.flushText()
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
		d.cmds = append(d.cmds, Command{Kind: KindType, Text: body})
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
	d.cmds = append(d.cmds, Command{Kind: KindKey, Keys: d.keys})
	d.keys = nil
}

// close flushes everything still buffered, including a held-back incomplete
// sequence, which at end of stream is as complete as it will ever get.
func (d *inputDecoder) close() {
	if len(d.pending) > 0 {
		stuck := d.pending
		d.pending = nil
		// A lone trailing ESC is the Esc key. Anything else is a sequence the
		// stream ended in the middle of: it is captured verbatim rather than
		// guessed at or dropped, so the tape still replays the exact bytes the
		// session produced.
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
	d.flushText()
	d.flushKeys()
}

// take returns the commands completed so far and clears them, leaving any
// in-progress text or key run buffered for the next chunk to continue.
func (d *inputDecoder) take() []Command {
	out := d.cmds
	d.cmds = nil
	return out
}
