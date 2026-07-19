# The tape language

A tape is a line-oriented script, one command per line, with `#` introducing a
comment. It covers exactly the harness primitives and nothing else: there is no
control flow, no variables, and no way to compute anything, because a test whose
expected screen depends on a computation is a test nobody can read.

```
# login.tape
Set Size 60 10
Spawn less README.md
Wait /tuitest/
Expect /headless testing harness/
Key q
ExpectExit 0
```

## Verbs

There are 19, and `Kind.Verb()` in [`tape/parse.go`](../tape/parse.go) is the
authoritative list.

| Verb | Argument shape | Effect |
| --- | --- | --- |
| `Set` | `KEY args...` | Configure the next `Spawn`. See below. |
| `Spawn` | `argv...` | Start the program under test. |
| `Type` | rest of line, verbatim | Send literal text, spacing preserved. |
| `Key` | key tokens | Send named keys or chords. |
| `Wait` | `[/re/] [+Screen\|+Line] [@dur]` | Block until the regex matches. |
| `WaitStable` | `[@dur]` | Block until output goes quiet. |
| `WaitOutput` | `[@dur]` | Block until the program writes something. |
| `WaitPrompt` | `[@dur]` | Block until an OSC 133 prompt is drawn. |
| `WaitCommand` | `[@dur]` | Block until an OSC 133 command finishes. |
| `Expect` | `/re/ [+Screen\|+Line]` | Assert the regex matches now, without waiting. |
| `ExpectExit` | `code` | Wait for exit and assert the status. |
| `Snapshot` | `name [+Styled]` | Compare the screen against a golden file. |
| `Resize` | `cols rows` | Change the terminal size mid-tape. |
| `Mouse` | `action button col row [+mods]` | Send one SGR mouse event. |
| `Paste` | Go-quoted string | Send text wrapped in bracketed-paste markers. |
| `Raw` | Go-quoted string | Write bytes to the child with no interpretation. |
| `Hide` | | Make subsequent `Snapshot` commands no-ops. |
| `Show` | | Undo `Hide`. |
| `Sleep` | duration | Sleep. Rejected under `-strict`. |

`Wait Stable` is an accepted spelling of `WaitStable` and takes the same
`@timeout`.

## Set

`Set` configures the terminal that the next `Spawn` creates, so every `Set` line
belongs above the `Spawn` it affects.

| Key | Value | Notes |
| --- | --- | --- |
| `Size` | `cols rows` | Each must be in 1..10000. |
| `Term` | a name | Default `xterm-256color`. |
| `Env` | `KEY=VALUE` | Repeatable; entries accumulate. |
| `WaitTimeout` | a duration | Default for wait-like verbs with no `@`. |
| `StabilizeInterval` | a duration | Quiet window for `WaitStable`. |

Durations must parse as a positive Go duration (`5s`, `250ms`). The size bound
exists because a tape is untrusted input to the CLI and the grid it names is
allocated up front, so an absurd size has to be rejected at parse time rather
than turned into a multi-gigabyte allocation. The same bound applies to
`Resize`.

## Wait modifiers

Wait-like verbs take up to three optional modifiers in any order after the verb:

- `/regex/`, read from the first slash on the line to the last, so the pattern
  may contain both spaces and slashes.
- `+Screen` or `+Line`, selecting whether the match runs against the whole
  screen or the last non-blank row. `+Screen` is the default.
- `@duration`, such as `@5s`, overriding `Set WaitTimeout` for that line.

Choosing between the wait verbs is the one judgement a tape author has to make.
`Wait /text/` is always the best option when you know what should appear.
`WaitOutput` is the right one after sending input when you do not know what the
reaction looks like. `WaitStable` is for after a burst of output with no
specific end state, and is the only one that can pass without the program having
done anything, because a screen that has been quiet all along is already stable.

## Keys

`Key` takes one or more tokens. A token is a named key, a bare rune, or a chord
of `Mod+Base`:

- Named: `Enter` (or `Return`), `Tab`, `Esc` (or `Escape`), `Space`,
  `Backspace`, `Delete`, `Insert`, `Up`, `Down`, `Left`, `Right`, `Home`, `End`,
  `PageUp`, `PageDown`, and `F1` through `F12`.
- Modifiers: `Ctrl` (or `C`), `Alt` (or `M`), `Shift` (or `S`), `Super`,
  `Hyper` and `Meta`. `Shift+` only has an effect on a lowercase letter.
- Anything else that is exactly one rune is sent as itself, so `Key %` works.
  A token ending in `+` names `+` itself, so `Key +` and `Key Alt++` both work.

An unknown key name is a parse error with the column of the offending token, not
a silent no-op at run time. So is a chord with no faithful encoding: `Ctrl++`
would have to send `0x0b`, which reads back as `Ctrl+k`, so it is rejected under
the legacy encoding rather than silently changing the key.

### Key attributes

A key reported by the kitty keyboard protocol can carry detail the legacy
encoding cannot express, written as trailing attributes:

```
Key a +Release
Key a +Repeat
Key a +Shifted A +Base a
Key a +Text "á"
```

`+Press`, `+Repeat` and `+Release` are the event type; `+Shifted` and `+Base`
are the key's alternate layouts; `+Text` is the text the keypress inserts, which
differs from the key itself for dead keys and input methods.

An attribute is a whitespace-separated token beginning with `+`, which cannot be
confused with a modifier because a modifier joins its key without spaces
(`Ctrl+b` is one token). A `Key` line carrying attributes names exactly one key,
so an attribute is never ambiguous about which keypress it qualifies.

See [input-protocols.md](input-protocols.md) for which encodings these map to,
the round-trip guarantees, and what happens when a tape is replayed against a
program that negotiates different keyboard modes than the recording did.

## Mouse

```
Mouse Press Left 10 5 +Ctrl
Mouse Release Left 10 5
Mouse Move Left 12 5
```

The action is `Press`, `Release` or `Move`; the button is `Left`, `Middle`,
`Right`, `WheelUp` or `WheelDown`; the coordinates are zero-based cells, encoded
1-based on the wire. `+Ctrl`, `+Alt` and `+Shift` are optional and repeatable.
The event is sent as an SGR (mode 1006) sequence unconditionally, so a program
that never enabled mouse reporting will not react to it.

## Paste and Raw

Both take a Go-quoted string, which is what lets them carry bytes no other verb
can express:

```
Paste "line one\nline two"
Raw "\x1b[1;2;3m"
Raw "\xc3\x28"
```

`Paste` wraps its text in bracketed-paste markers (mode 2004). `Raw` writes the
bytes exactly as given, including malformed UTF-8 and truncated escape
sequences. `Raw` is what the fuzzer's hostile payloads are written as, which is
why a minimised reproduction is an ordinary tape any user can read and rerun.

## Snapshots

`Snapshot name` compares the plain screen against `testdata/name.golden`;
`Snapshot name +Styled` compares the styled encoding instead (see
[api.md](api.md#snapshots-and-golden-files) for the format). `-golden-dir`
changes the directory and `-update` rewrites the files.

`Hide` and `Show` bracket a region whose snapshots should not run, which is how
you keep setup steps out of the golden set without deleting the commands that
perform them.

## Parse errors

A tape that will not parse exits 2 and reports file, line, and column with a
caret under the token:

```
tuitest: login.tape:3:11: unexpected token "+Screne" (want /regex/, +Screen, +Line, or @timeout)
  3 | Wait /ok/ +Screne @5s
    |           ^
```

A misspelled verb gets a nearest-match suggestion, computed against the same
verb table the parser dispatches on, so the suggestion cannot drift from
reality.

## Round-tripping

`tape.Parse` and `tape.Sprint` are inverses over the canonical form, which is
checked by `tape/roundtrip_test.go` and `tape/print_roundtrip_test.go`. That is
what makes `tuitest record` trustworthy: anything the recorder writes, the
parser reads, and printing a parsed tape reproduces it. It is also why a
recording is meant to be read and edited rather than treated as an opaque
artifact.
