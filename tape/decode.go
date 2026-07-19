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

	// dropped counts input sequences with no tape representation, such as
	// mouse reports. The recorder surfaces this so a recording that silently
	// lost input is not mistaken for a faithful one.
	dropped int
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
func (d *inputDecoder) decodeEscape(rest []byte) (n int, hold bool) {
	for _, seq := range escSeqByLen {
		if strings.HasPrefix(string(rest), seq) {
			d.emitKey(escSeq[seq])
			return len(seq), false
		}
	}

	// An incomplete CSI or SS3 introducer: hold it and retry with more bytes.
	if incompleteEscape(rest) {
		return 0, true
	}

	// A complete but unrepresentable sequence (mouse report, bracketed paste,
	// kitty key). Consume it so its bytes do not leak into a Type command.
	if len(rest) >= 2 && (rest[1] == '[' || rest[1] == 'O') {
		if end := csiEnd(rest); end > 0 {
			d.flushText()
			d.dropped++
			return end, false
		}
	}

	// ESC + printable rune within one chunk is the Alt (meta) encoding.
	if len(rest) >= 2 && rest[1] >= 0x20 && rest[1] != 0x7f {
		if rest[1] >= utf8.RuneSelf && !utf8.FullRune(rest[1:]) {
			return 0, true
		}
		r, size := utf8.DecodeRune(rest[1:])
		d.emitKey("Alt+" + string(r))
		return 1 + size, false
	}

	d.emitKey("Esc")
	return 1, false
}

// incompleteEscape reports whether rest is a proper prefix of a sequence that
// more bytes could still complete. A bare trailing ESC is deliberately not
// treated as incomplete: it is the Esc key.
func incompleteEscape(rest []byte) bool {
	if len(rest) < 2 {
		return false
	}
	if rest[1] != '[' && rest[1] != 'O' {
		return false
	}
	// Still inside the parameter bytes of a CSI/SS3 with no final byte yet.
	return csiEnd(rest) == 0
}

// csiEnd returns the length of the CSI or SS3 sequence at the head of rest, or
// 0 if it is not yet terminated. A final byte is in the range 0x40-0x7e.
func csiEnd(rest []byte) int {
	for i := 2; i < len(rest); i++ {
		if rest[i] >= 0x40 && rest[i] <= 0x7e {
			return i + 1
		}
	}
	return 0
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
		// A lone trailing ESC is the Esc key; a truncated multi-byte sequence
		// has no faithful representation, so it is counted rather than guessed.
		if len(stuck) == 1 && stuck[0] == 0x1b {
			d.emitKey("Esc")
		} else {
			d.dropped++
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
