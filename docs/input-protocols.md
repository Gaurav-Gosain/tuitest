# Input protocols

The recorder turns a live session into a tape. This document describes how it
decodes the input stream, what guarantees the result carries, and what it takes
to add support for a protocol.

## Completeness by construction

The central guarantee is that **a recording never loses input**. It does not
depend on how many protocols are supported, and it does not degrade when a
terminal invents a new sequence.

Decoding proceeds in three steps, and the last one cannot fail:

1. Every registered protocol is offered the bytes. The longest match wins.
2. If no protocol claims them, an ECMA-48 framer finds where the sequence ends
   from its *shape* alone, without knowing what it means. Those bytes become a
   `Raw` command, which replays byte for byte.
3. If the bytes are not a control sequence either, one byte becomes `Raw`.

So semantic decoding is a **readability** layer, never a correctness
requirement. Supporting a protocol changes `Raw "\x1b[97;5u"` into
`Key Ctrl+a`; it does not change whether the tape replays correctly.

This is why there is no "dropped sequences" warning any more. Input that cannot
be named is captured rather than discarded, so the count would always be zero.

### Why this matters more than coverage

A dropped sequence announces itself: the tape is visibly short. A *misdecoded*
one does not. The recorder that motivated this design decoded a kitty graphics
capability reply, `ESC _ Gi=1;OK ESC \`, as three keystrokes:

```
Key Alt+_
Type Gi=1;OK
Key Alt+\
```

That tape looks plausible and replays nonsense. Programs query the terminal
constantly (device attributes, cursor position, colours, `XTVERSION`, kitty
graphics and keyboard support) and the replies arrive on the input channel. A
decoder that reads a reply as keystrokes is actively wrong.

Two invariants keep that from recurring, both fuzz-checked:

- **Only keyboard protocols may emit `Key` or `Type`.** Any other sequence class
  decodes to something else, or to `Raw`. (`FuzzNonKeyboardIsNeverKeyboard`)
- **A decode that cannot be re-encoded is rejected.** The dispatcher verifies
  every candidate decode round-trips before accepting it, and falls back to
  `Raw` when it does not, so a buggy protocol costs readability rather than
  fidelity. (`FuzzRecorderIsLossless`)

## The round-trip property

The correctness backbone has two directions.

**Command direction**, exact, no exceptions: for every key a tape can express,
decoding its encoding returns the same command. `FuzzKeyCommandRoundTrip`
checks this over fuzzed tokens; `TestModifierMatrixRoundTrips` and
`TestKittyEventMatrixRoundTrips` check it over the full key/modifier/event
matrix.

**Byte direction**, two tiers:

| Tier | Guarantee | Applies to |
| --- | --- | --- |
| `Exact` | `Encode(Decode(b)) == b` | `Raw`, and any protocol declaring it |
| `Canonical` | `Decode(Encode(Decode(b))) == Decode(b)` | legacy keys, modifyOtherKeys, kitty |

`Canonical` exists because the wire format has redundant spellings and the tape
records *the key*, not the spelling. The normalizations are:

- an omitted default parameter is restored: `CSI A` and `CSI 1 A` both decode to
  `Up`;
- a modified SS3 key is promoted to its CSI form, since SS3 has nowhere to put a
  parameter, which is what real terminals do;
- under kitty, a key may be re-encoded in the `CSI u` form even when the
  recording used the legacy spelling, because both are valid and mean the same
  key to a program that negotiated kitty.

"Equivalent" is defined precisely: two command sequences are equivalent when
they send the same bytes in the same order. How consecutive keys are *grouped*
onto `Key` lines is not significant, since `Key a` + `Key b` and `Key a b` are
the same input. Everything else is compared strictly.

## Supported keyboard protocols

### Legacy

C0 control bytes, the ESC-prefixed meta encoding of Alt, and the CSI/SS3 cursor
and function keys with the xterm modifier parameter (the modifier bitmask plus
one). Application cursor mode (DECCKM) is honoured in both directions: `Up` is
`CSI A` normally and `SS3 A` under DECCKM, and both decode to `Up`.

### modifyOtherKeys

xterm's `CSI 27 ; mod ; code ~`, which exists so a program can tell `Ctrl+I`
from `Tab` and `Ctrl+M` from `Enter`. The legacy encoding collapses those onto
one control byte, so without this protocol the distinction is unrecoverable.

### Kitty keyboard

The `CSI u` encoding, including progressive enhancement flags, press/repeat/
release events, alternate (shifted and base layout) keys, and associated text:

```
CSI code[:shifted[:base]] [; mods[:event]] [; text[:text...]] final
```

The tape's `Key` verb carries the extra detail as trailing attributes rather
than through a parallel verb:

```
Key a +Release
Key a +Repeat
Key a +Shifted A +Base a
Key a +Text "á"
```

An attribute is a whitespace-separated token beginning with `+`, which cannot be
confused with a modifier because modifiers join to their key without spaces
(`Ctrl+b` is one token). **A `Key` line carrying attributes names exactly one
key**, so an attribute is never ambiguous about which keypress it qualifies; the
parser rejects the alternative with a message saying so.

## Mode context, and replaying under different modes

The same bytes are a different key depending on what the program negotiated.
`CSI A` and `SS3 A` are both `Up`; `0x09` is `Tab` but `CSI 27;5;105~` is
`Ctrl+i`. So the decoder cannot be a pure function of the input stream: it is
told the modes observed on the child's *output* stream, via
`Recorder.SetModes`.

Each recorded command also remembers the modes it was decoded under, and a
protocol may infer a mode from the sequence itself: a kitty key report is proof
kitty was negotiated, even if the mode mirror missed the enabling sequence.

### What happens when replay negotiates different flags

This case is subtle and worth stating plainly.

A tape stores *keys*, not bytes (except for `Raw`, which stores bytes).

**What the file carries.** The mode context a key was decoded under lives in
memory during recording but is **not** written to the tape. So a plain chord
recorded under kitty comes back as `Key Ctrl+a` with no modes and replays in the
legacy spelling `0x01` rather than `CSI 97;5u`. That is behaviourally
equivalent, since a program that negotiated kitty still accepts the legacy
encoding, and it keeps recordings readable and portable.

What does survive is everything the legacy encoding *cannot* express, because
that is information rather than spelling. Those keys carry attributes, and
attributes are written to the file and force the kitty encoding on replay:

| Recorded | Tape line | Replays as |
| --- | --- | --- |
| `CSI 97;5u` | `Key Ctrl+a` | `0x01` (legacy spelling) |
| `CSI 97;1:3u` | `Key a +Release` | `CSI 97;1:3u` (exact) |
| `CSI 97;;225u` | `Key a +Text "á"` | `CSI 97;;225u` (exact) |

`TestWhatSurvivesTheTapeFile` pins this, so it is a contract rather than an
accident.

**What happens at replay time.** Each key is re-encoded from what the tape says,
**not** re-interpreted under whatever the program negotiates this time. The
consequences:

- **The program negotiates the same modes.** Everything replays as recorded.
  This is the normal case.

- **The program negotiates fewer modes than the recording.** Say the recording
  was made with kitty enabled and this build no longer enables it. The tape will
  send `CSI u` sequences to a program that did not ask for them. A program that
  ignores unknown CSI sequences will see nothing; one that echoes them will
  misbehave. This is a genuine behaviour change in the program under test, and
  the tape failing is the correct outcome rather than something to paper over.

- **The program negotiates more modes than the recording.** The tape sends
  legacy encodings, which every terminal-aware program still accepts, so replay
  works. What is lost is coverage rather than correctness: the recording cannot
  exercise the release events the program can now receive, because nothing ever
  sent one. Re-record to cover the new capability.

- **`Raw` commands are unaffected.** They replay byte for byte under all
  circumstances, which is what makes the fallback trustworthy.

The one thing the tape deliberately does **not** do is re-encode a key under the
replay-time modes. Doing so would silently change what the program under test
receives, turning a mode regression into a passing test.

## Adding a protocol

One self-contained file, and one `Register` call in its `init`. No changes to
the recorder, the parser, the player, or any other protocol.

```go
func init() { Register(myProtocol{}) }

type myProtocol struct{}

func (myProtocol) Name() string       { return "my-protocol" }
func (myProtocol) Priority() int      { return 30 }
func (myProtocol) Fidelity() Fidelity { return Exact }

func (myProtocol) Decode(buf []byte, m Modes) (int, []Command, Result) {
	// NoMatch: not mine. Partial: mine but incomplete, hold for more bytes.
	// Full: consumed n bytes, here are the commands.
}

func (myProtocol) Encode(c Command, m Modes) ([]byte, bool) {
	// false when the command is not mine.
}
```

Rules the dispatcher enforces so protocols cannot interfere with each other:

- **Longest `Full` match wins**, ties broken by `Priority` (higher first), then
  registration order. Claim only what you are sure of; returning `NoMatch` is
  always safe because the framer backstops you.
- **Return `NoMatch` for a sequence you recognize but cannot re-encode.** The
  dispatcher checks this anyway and demotes you to `Raw`, but returning it
  yourself is clearer.
- **Only keyboard protocols may emit `KindKey` or `KindType`.** This is
  fuzz-enforced.
- Use `csiFrame` rather than parsing the CSI shape yourself. A CSI aborted by an
  illegal byte has no final byte at all, and `csiFrame` handles that.

## Not supported, and why

These fall through to `Raw` and still replay byte for byte.

| Sequence | Reason |
| --- | --- |
| UTF-8 mouse (mode 1005) | Ambiguous with typed text by construction. |
| X10 mouse coordinates above 223 | Unrepresentable on the wire. |
| 8-bit meta (high-bit-set bytes) | Indistinguishable from Latin-1 text. |
| 8-bit C1 introducers | Framed and captured, but not decoded semantically. |
| Kitty functional codes outside the table | A code in the Private Use Area block that this table does not name is a key from a newer protocol revision. Spelling it as the PUA character it equals would be a lie. |
| `Alt` plus a control-string introducer | `Alt+Shift+p` sends `ESC P`, which is also DCS; `Alt+Shift+x` sends `ESC X`, which is also SOS. Encoding is faithful, since that is what a terminal sends, but decoding must prefer the control string. |

Two of these deserve the reasoning spelled out, because both are cases where
the wire form is genuinely ambiguous and the decoder has to choose.

**`CSI R` is both the cursor position report and F3.** A function key in the
SS3 family reaches the CSI form only with a real modifier, because its
unmodified spelling is SS3. So `SS3 R` and `CSI 1;5R` are F3 and `Ctrl+F3`,
while `CSI R`, `CSI 1;R` and `CSI 1;1R` are all position reports, the last one
for row 1, which is exactly the case a naive "explicit parameter" rule gets
wrong. The same collision exists on `CSI S`.

**`ESC P` is both `Alt+Shift+p` and DCS.** Here the decoder prefers the control
string, and the reasoning is the asymmetry this whole design rests on. Choosing
"key" when the bytes were a device reply corrupts the tape silently. Choosing
"control string" when the bytes were a keypress costs only readability, because
the bytes still replay exactly as `Raw`. When the cost of being wrong is
lopsided, take the side that fails visibly.

Both chords are unambiguous under kitty, which is what the protocol is for.

The last row is also why a literal Private Use Area character is never encoded
as a kitty key code: the reader would take it for a function key.
