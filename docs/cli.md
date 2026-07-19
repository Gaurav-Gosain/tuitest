# The command line

`tuitest` is usable on its own: you can test a TUI, or only look at one, without
writing any Go.

```
go install github.com/Gaurav-Gosain/tuitest/cmd/tuitest@latest
```

The command set is a registry ([`internal/cli/cli.go`](../internal/cli/cli.go)),
so help, shell completion, and the "did you mean" suggestion for an unknown
command are all generated from the same list the dispatcher resolves against and
cannot fall out of step with it.

## Exit codes

| Code | Kind | Meaning |
| ---- | ---- | ------- |
| 0 | `ok` | every assertion passed |
| 1 | `assertion` | `Expect`, `Snapshot` or `ExpectExit` did not hold, or the program exited before a wait was satisfied |
| 2 | `usage` | bad usage, or a tape that would not parse |
| 3 | `harness` | no PTY, a program that would not start, an unreadable golden file |
| 4 | `timeout` | a wait exceeded its deadline |

The split matters to a script: 1 means the program under test is wrong, 3 means
tuitest could not do its job, and reacting to those the same way hides real
failures behind infrastructure noise. `replay -step` additionally exits 130 when
the operator abandons a stepped run, the conventional status for "interrupted".

## snap

The fastest way to see what a TUI actually draws. It spawns the program, waits
until it stops drawing, prints the screen, and exits, asserting nothing.

```
tuitest snap -- htop
tuitest snap -size 120x40 -- vim
tuitest snap -wait '\$ ' -- bash -i          # settle on a prompt instead of on quiet
tuitest snap -type 'hello\r' -- ./myapp      # send input first, then capture
tuitest snap -styled -- ./myapp              # keep the SGR styling
tuitest snap -json -- ./myapp | jq -r .screen
```

| Flag | Default | Effect |
| --- | --- | --- |
| `-size COLSxROWS` | `80x24` | Terminal size. |
| `-term NAME` | `xterm-256color` | `TERM` for the program. |
| `-env KEY=VALUE` | | Repeatable environment entry. |
| `-dir PATH` | | Working directory for the program. |
| `-timeout DUR` | `5s` | How long to wait for the screen to settle. |
| `-settle DUR` | `150ms` | Quiet window that counts as settled. |
| `-wait RE` | | Wait until this regex matches instead of waiting for quiet. |
| `-type TEXT` | | Type this first (`\r`, `\n`, `\t`, `\e` unescaped). |
| `-styled` | off | Render SGR styling instead of plain text. |
| `-json` | off | Print one JSON object to stdout. |

Put `--` before the program so its own flags are not read as tuitest's.

`-type` waits for the program to react before capturing. The screen has usually
been quiet for longer than the settle window by the time the input is sent, so a
naive capture would show the screen as it was beforehand. When the specific
response matters, pair it with `-wait`.

## run

Plays a tape headlessly. This is the CI command.

```
tuitest run login.tape
tuitest run -update login.tape           # rewrite the golden files
tuitest run -strict login.tape           # reject Sleep, forcing real waits
tuitest run -size 120x40 login.tape      # override the tape's Set Size
tuitest run -timeout 10s login.tape      # override the tape's Set WaitTimeout
tuitest run -env NO_COLOR=1 login.tape   # repeatable
tuitest run -json login.tape             # one JSON object, for CI
tuitest run -v login.tape                # mirror PTY traffic to stderr
```

| Flag | Default | Effect |
| --- | --- | --- |
| `-update` | off | Rewrite goldens instead of comparing. |
| `-strict` | off | Treat `Sleep` as an error. |
| `-golden-dir DIR` | `testdata` | Where goldens live. |
| `-size COLSxROWS` | from the tape | Override `Set Size`. |
| `-term NAME` | from the tape | Override `Set Term`. |
| `-timeout DUR` | from the tape | Override `Set WaitTimeout`. |
| `-env KEY=VALUE` | | Additional environment entry, repeatable. |
| `-v` | off | Mirror PTY I/O to stderr. |
| `-json` | off | Print one JSON object to stdout. |

A flag beats the tape's own `Set` line for the same setting rather than merging
with it, which is the direction that makes `-size 120x40` useful for checking a
layout at a second size without editing the file. `-env` accumulates instead,
since environment entries add up rather than replacing each other.

## record

Spawns a program, connects it to your terminal so you can drive it normally, and
writes what you did as a tape. `Ctrl+]` ends the recording.

```
tuitest record -o login.tape -- ./myapp
tuitest record -snapshots -o login.tape -- ./myapp   # also write the goldens
tuitest record -cols 80 -rows 24 -o t.tape -- vim    # record at a fixed size
tuitest record -- htop                               # write the tape to stdout
```

| Flag | Default | Effect |
| --- | --- | --- |
| `-o PATH` | stdout | Where to write the tape. |
| `-snapshots` | off | Emit a `Snapshot` at each settle point and write its golden. |
| `-golden-dir DIR` | `testdata` | Where `-snapshots` writes goldens. |
| `-cols N`, `-rows N` | this terminal | Record at a fixed size. |
| `-term NAME` | `xterm-256color` | `TERM` for the recorded program. |
| `-quiet DUR` | `120ms` | How long the screen must hold still to count as settled. |
| `-idle-sleep DUR` | `0` (disabled) | Emit `Sleep` for silent pauses at least this long. |

The interesting part is what it does about timing, because that is the
difference between a tape that documents a session and one worth running in CI.
Wherever the screen settles, the recorder prefers a `Wait` on text that is both
new and distinctive; if nothing anchorable appeared it falls back to
`WaitStable`; and if the screen never changed at all it emits nothing, since
your thinking time is not part of the test. It never writes a `Sleep` unless you
ask for one with `-idle-sleep`. An anchor that already matched the previous
screen is rejected, because a wait on text that is already there passes
instantly and synchronises nothing. Anchors shorter than three runes are
rejected too: they are usually prompts or box drawing, and they make brittle
tapes.

`-snapshots` captures the screen behind each settle point and writes the golden
files at the same time, so a recording replays green immediately instead of
needing a separate `-update` pass.

A recording is meant to be read and edited. It is written with a header saying
where it came from, and the waits are the part worth skimming before you trust
it as a test.

## replay

Plays a tape onto your terminal and renders the program as it goes, so you can
watch what a tape does rather than reading its assertions.

```
tuitest replay login.tape
tuitest replay -speed 2 login.tape      # divide every Sleep by 2
tuitest replay -step login.tape         # pause before each command
tuitest replay -update login.tape       # rewrite the golden files
```

| Flag | Default | Effect |
| --- | --- | --- |
| `-speed N` | `1` | Divide every `Sleep` by N. |
| `-step` | off | Pause before each command until you press enter. |
| `-echo` | on | Print each command as it runs. |
| `-width N` | `40` | Column width for side-by-side failure output. |
| `-golden-dir DIR` | `testdata` | Where goldens live. |
| `-update` | off | Rewrite goldens instead of comparing. |
| `-strict` | off | Treat `Sleep` as an error. |

`-speed` scales `Sleep` only; it cannot make the program under test faster, and
every other wait still blocks on its real condition. Replay wraps the same
player `run` uses, so what you watch is what a headless run does. When an
assertion fails it shows the expected and actual screens in two columns with a
`|` against every row that differs, which is easier to read than a line diff
when the difference is a matter of layout.

## fuzz

See [fuzzing.md](fuzzing.md) for what it sends, what it detects, and how
reproductions are minimised.

```
tuitest fuzz -- ./myapp
tuitest fuzz -seed 42 -iterations 200 -corpus testdata/fuzz -- ./myapp
tuitest fuzz -duration 5m -exclude Ctrl+c -- ./myapp
```

| Flag | Default | Effect |
| --- | --- | --- |
| `-seed N` | time-based | PRNG seed for a reproducible session. |
| `-iterations N` | `50` | How many programs to spawn and drive (0 for unlimited). |
| `-duration DUR` | `0` | Wall-clock budget (0 for unlimited). |
| `-cols N`, `-rows N` | `80`, `24` | Initial terminal size. |
| `-actions N` | `60` | Maximum input actions per iteration. |
| `-corpus DIR` | | Where reproductions are written and replayed from. |
| `-exclude LIST` | | Key tokens never to send, for example `Ctrl+c,q`. |
| `-no-hostile` | off | Do not send malformed or oversized escape sequences. |
| `-no-mouse`, `-no-resize` | off | Narrow the input space. |
| `-no-shrink` | off | Do not minimise a failing input. |
| `-shrink-budget N` | `200` | Maximum replays spent minimising one failure. |
| `-hang-after DUR` | `5s` | Silence from a live program that counts as a hang. |
| `-settle DUR` | `2s` | Bound on each wait inside an iteration. |
| `-max-memory-growth F` | `0` (off) | Fail if RSS grows by this factor. Linux only. |
| `-allow-dirty-exit` | off | Do not report a program that leaves the terminal dirty. |
| `-stop-on-first` | off | Stop at the first failure instead of looking for distinct ones. |
| `-q` | off | Only print the summary. |

At least one of `-iterations` and `-duration` must bound the run; setting both
to zero is an error rather than an infinite loop. Ctrl+C ends a session cleanly,
so an interrupted run still reports and still keeps the corpus entries it found.
Without `-corpus` there is nowhere to write a reproduction, so it is printed to
stdout instead: a failure without a reproduction is not actionable.

## doctor

Reports whether tuitest will work here and whether tests will be stable: PTY
allocation, the platform, `TERM`, the size the program under test will get, what
the bundled emulator understands, a writable temp directory, usable CPU count,
whether a Go toolchain is on `PATH`, and whether `CI` is set. It spawns no
program and writes no files, so it is safe as a CI preflight step.

```
tuitest doctor
tuitest doctor -json | jq '.checks[] | select(.status != "ok")'
```

It exits 3 when a check fails, so `tuitest doctor || exit 1` gates a build. The
PTY check is the one that actually matters: it allocates a pseudo-terminal and
closes it immediately, and its hint names the usual cause (a container without
`/dev/pts` mounted). Everything else is a warning at worst, because a
single-CPU runner or a missing Go toolchain makes a suite slower or narrower
rather than impossible.

## Machine-readable output

`-json` on `run`, `snap`, and `doctor` prints one indented object to stdout,
which is both readable in a terminal and valid input for `jq`. `run` reports the
status, the `kind` matching the exit code, the duration, and the full error text
including the screen at the moment of failure:

```json
{
  "command": "run",
  "tape": "login.tape",
  "status": "fail",
  "kind": "assertion",
  "exitCode": 1,
  "durationMs": 214,
  "error": "tape line 6: ExpectExit failed\n  want: exit status 0\n  got:  exit status 3"
}
```

## Error messages

A failure has to be readable without rerunning anything, so each kind prints
what a person actually needs.

A timeout says what it was waiting for, how long it waited, and what was on
screen at that moment:

```
tuitest: tape line 3: WaitForMatch timed out after 801ms waiting for match nevergonnahappen
--- screen ---
ECHOTUI
>
```

A failed `Expect` contrasts expected with actual and marks where they diverge.
For a literal pattern it finds the closest line on screen; for a pattern with
metacharacters there is no single expected string, so it shows the screen
instead of inventing a comparison:

```
tuitest: tape line 7: Expect failed
  want: regex /echo: hj/ to match the whole screen
  got:  no match
  the closest line on screen was:
    want | echo: hj
    got  | echo: hi
         |        ^ first difference at column 8
```

The library prefixes its own errors with `tuitest: ` so they stand out in Go
test output; on the command line the program name is already printed, so only a
leading prefix is trimmed. A screen dump that happens to contain the word is
left alone.

## Shell completion

Completion is generated from the command registry, so it never falls out of step
with the commands themselves.

```
tuitest completion bash > /etc/bash_completion.d/tuitest
tuitest completion zsh  > "${fpath[1]}/_tuitest"
tuitest completion fish > ~/.config/fish/completions/tuitest.fish
```
