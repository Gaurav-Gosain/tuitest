# tuitest

tuitest is a headless testing harness for terminal programs. It runs the program
under test in a real pseudo-terminal, interprets its output with a VT emulator,
and lets you assert on the resulting screen as a grid of cells rather than as a
raw byte stream.

It is for anyone who has a terminal program and wants to test what a user would
actually see: TUI authors, shell tooling authors, and people maintaining
terminal multiplexers. It has no knowledge of any particular UI framework, so it
works equally against a Bubble Tea app, a Rust or C TUI, vim, or a bare shell.

There are two ways to use it, and neither is the lesser one. The `tuitest`
command tests a TUI from a script called a tape, with no Go anywhere; the Go
package does the same thing from a test function when you want the program's
own language. The command is not a demo of the library, it is the interface
most people should reach for first.

It also fuzzes. `tuitest fuzz` points randomised but structured input at any
terminal program and hunts for crashes, hangs, and terminals left in a broken
state, then minimises whatever it finds into a tape file that replays it. See
[Fuzzing a TUI](#fuzzing-a-tui).

## Requirements

- A Unix-like OS that can open PTYs (`/dev/ptmx`). Windows is not supported and
  deliberately fails to build; see Limitations.
- Go 1.25 or newer to install, and to use the package. Running tapes needs no Go.

## Installation

The command-line tool, which tests a TUI without any Go:

```
go install github.com/Gaurav-Gosain/tuitest/cmd/tuitest@latest
```

The Go package, for tests written in Go:

```
go get github.com/Gaurav-Gosain/tuitest
```

## Quick start, without writing any Go

Check that the environment can run a TUI at all. It exits non-zero if not, so
it works as a CI gate:

```
tuitest doctor
```

Look at what a program actually draws. This writes nothing and asserts nothing,
and is the fastest way to find the text a test could wait for:

```
tuitest snap -- htop
```

Write that as a test. A tape is one command per line:

```
# login.tape
Set Size 60 10
Spawn less README.md
Wait /tuitest/
Expect /headless testing harness/
Key q
ExpectExit 0
```

Run it. It exits 0 when every assertion holds, and prints the screen when one
does not:

```
tuitest run login.tape
```

If you would rather not write the tape by hand, record one. This connects the
program to your terminal, you drive it normally, and it writes down what you
did; press Ctrl+] to stop:

```
tuitest record -o login.tape -- ./myapp
```

Watch a tape run, which is the quickest way to see why one fails:

```
tuitest replay login.tape
```

And point the fuzzer at the program to find what no one thought to test:

```
tuitest fuzz -duration 30s -corpus ./corpus -- ./myapp
```

That is the whole loop: `snap` to look, `record` or an editor to write, `run` in
CI, `replay` to debug, `fuzz` to go looking for trouble.

## A minimal example

```go
package myapp_test

import (
    "testing"
    "time"

    "github.com/Gaurav-Gosain/tuitest"
)

func TestGreeting(t *testing.T) {
    term := tuitest.StartT(t, []string{"./myapp"}, tuitest.WithSize(80, 24))

    if err := term.WaitForText("ready", 5*time.Second); err != nil {
        t.Fatal(err)
    }

    term.SendKeys("hello", tuitest.Enter)

    if err := term.WaitForText("you said hello", 3*time.Second); err != nil {
        t.Fatal(err)
    }
}
```

`StartT` spawns the program, mirrors PTY traffic into `t.Log`, registers
teardown through `t.Cleanup`, and fails the test if the spawn itself fails.
There are no sleeps: each wait blocks on a condition and returns as soon as it
holds.

When a wait times out, the error carries the screen and the tail of the I/O log,
so a CI failure looks like this rather than a bare `timeout`:

```
tuitest: WaitForText timed out after 3s waiting for text "you said hello"
--- screen ---
myapp v0.2
ready
> hello
--- last I/O ---
...
```

## A realistic example

The thing tuitest is actually good at is asserting on a full-screen program's
rendered state, including styling, while it is being driven and resized. This
test drives a program with the keyboard and the mouse, resizes it mid-flight,
waits for the redraw to settle, and pins the result to a golden file.

```go
func TestPaneSurvivesResize(t *testing.T) {
    term := tuitest.StartT(t, []string{"./myapp"},
        tuitest.WithSize(120, 40),
        tuitest.WithTrueColor(),
        tuitest.WithDir(t.TempDir()),
    )

    if err := term.WaitForText("ready", 10*time.Second); err != nil {
        t.Fatal(err)
    }

    // Open the menu with a chord, then click an entry.
    term.SendKeys(tuitest.Ctrl('b'), "m")
    if err := term.WaitForText("Preferences", 5*time.Second); err != nil {
        t.Fatal(err)
    }
    term.SendMouse(tuitest.MouseEvent{
        Col: 12, Row: 4,
        Button: tuitest.MouseLeft,
        Action: tuitest.MousePress,
    })

    // Shrink the terminal underneath it. The child gets a real SIGWINCH.
    if err := term.Resize(60, 20); err != nil {
        t.Fatal(err)
    }

    // Wait for the specific post-resize content, not merely for quiet.
    if err := term.WaitForMatch(
        regexp.MustCompile(`Preferences\s*$`),
        tuitest.ScopeScreen,
        5*time.Second,
    ); err != nil {
        t.Fatal(err)
    }

    // Assert the styled grid: text plus per-row attribute runs.
    term.AssertGoldenStyled(t, "prefs-60x20")
}
```

Run it once with `UPDATE_GOLDEN=1 go test ./...` to record
`testdata/prefs-60x20.golden`, then review that file as part of the diff. A
styled golden looks like this, with each row's text followed by indented
attribute runs for spans that differ from the default style:

```
 Preferences
    1-11 b,fg:15,bg:4
 Theme:  dark
    9-12 fg:2
```

## API

### Spawning

```go
func Start(argv []string, opts ...Option) (*Terminal, error)
func StartT(tb testing.TB, argv []string, opts ...Option) *Terminal
```

`Start` is the plain constructor. `StartT` is the one to use under `go test`.

Options:

| Option | Effect |
| --- | --- |
| `WithSize(cols, rows int)` | Initial PTY size. Default 80x24. |
| `WithEnv(kv ...string)` | Add or override `KEY=VALUE` entries. |
| `WithInheritEnv()` | Start from the parent environment instead of the hermetic default. |
| `WithDir(path string)` | Child working directory. |
| `WithTerm(term string)` | `TERM` value. Default `xterm-256color`. |
| `WithTrueColor()` | Sets `COLORTERM=truecolor`. |
| `WithLog(w io.Writer)` | Mirror all PTY I/O to `w`. |
| `WithSemanticMarkers()` | Enable the OSC 133 waits. |
| `WithStabilizeInterval(d time.Duration)` | Quiet window for `WaitStable`. Default 150ms. |

By default the child gets a minimal hermetic environment (`PATH`, `HOME`,
`TERM`, `LANG=C.UTF-8`) so a developer's shell configuration cannot change the
result.

### Input

```go
func (t *Terminal) SendKeys(items ...any) error
func (t *Terminal) Type(s string) error
func (t *Terminal) SendMouse(ev MouseEvent) error
func (t *Terminal) Paste(s string) error
func (t *Terminal) Resize(cols, rows int) error
```

`SendKeys` accepts strings, runes, `Key` values, and slices of those. Plain text
is sent literally; a `Key` carries its own escape sequence. `Type` never
interprets key names, which matters when the text itself could look like one.

Named keys are typed constants, so a misspelling is a compile error rather than
a silent mismatch: `Enter`, `Tab`, `Esc`, `Space`, `Backspace`, `Delete`, `Up`,
`Down`, `Left`, `Right`, `Home`, `End`, `PageUp`, `PageDown`, `Insert`, and
`F1` through `F12`. `Ctrl(r rune)` builds a control byte (`Ctrl('b')` is 0x02)
and `Alt(k any)` prefixes with ESC.

`SendMouse` emits SGR (mode 1006) sequences. It writes them unconditionally, so
a program that never enabled mouse reporting simply will not react.

`Paste` wraps text in bracketed-paste markers (mode 2004), the way a terminal
delivers a real paste. Programs take a different code path for pasted text than
for typed text, and it is usually the less tested one.

### Waiting

```go
func (t *Terminal) WaitForText(substr string, timeout time.Duration) error
func (t *Terminal) WaitForMatch(re *regexp.Regexp, scope Scope, timeout time.Duration) error
func (t *Terminal) WaitFor(cond func(Screen) bool, timeout time.Duration) error
func (t *Terminal) WaitStable(timeout time.Duration) error
func (t *Terminal) WaitExit(timeout time.Duration) (int, error)
```

`WaitStable` and `WaitForOutput` answer different questions and are easy to
confuse. `WaitStable` waits for output to go quiet, which is what you want after
a burst. `WaitForOutput` waits for the program to write something after the call
begins, which is what you want after sending input: once the screen has settled,
`WaitStable` is already satisfied and returns without the program having done
anything at all.

`Scope` is `ScopeScreen` (the whole screen) or `ScopeLastLine` (the last
non-blank row, useful for prompts). A wait that times out returns
`*TimeoutError`; a wait whose child exits first returns `*ClosedError`. Both
embed a screen dump and the recent I/O.

Both unwrap to a sentinel, so the kind of failure is matchable without a type
assertion:

```go
switch err := term.WaitForText("ready", 5*time.Second); {
case errors.Is(err, tuitest.ErrTimeout):
    // still running, just not there yet
case errors.Is(err, tuitest.ErrChildExited):
    // it died; err's message carries the exit code and the last screen
}
```

The sentinels are `ErrTimeout`, `ErrChildExited`, and `ErrSemanticMarkers`, the
last returned by the OSC 133 waits when `WithSemanticMarkers` was not passed.

`WaitStable` measures its quiet window from the later of the last output byte
and the last input tuitest sent, so calling it straight after `SendKeys` cannot
report the pre-keystroke screen as stable. It remains a heuristic: a program
that takes longer than the interval to produce its first byte is reported
stable too early. Wait for the content you expect whenever you know it.

`WaitExit` waits for process exit rather than for screen state. `Wait` is a
deprecated alias for it.

With `WithSemanticMarkers`, OSC 133 shell integration adds:

```go
func (t *Terminal) WaitForPrompt(timeout time.Duration) error
func (t *Terminal) WaitForCommand(timeout time.Duration) error
func (t *Terminal) LastCommandExit() (code int, ok bool)
```

These synchronize on the shell's own prompt and command-finished markers, which
is far more reliable than matching prompt text. Without the option they return
an error rather than silently never firing.

### Terminal state and exit status

```go
func (t *Terminal) TermState() TermState
func (t *Terminal) ExitStatus() (ExitStatus, bool)
func (t *Terminal) Progress() (bytes int64, last time.Time)
```

`TermState` reports the modes a program has left set: the alternate screen,
mouse tracking, bracketed paste, focus reporting, and cursor visibility. Called
after the child exits, `Dirty()` answers "did this program restore the
terminal?", which is a common and user-visible bug: a TUI that exits without
leaving the alternate screen or with mouse reporting still on leaves the user's
shell unusable.

`ExitStatus` separates a signal death from an ordinary non-zero exit, which
`ExitCode` alone flattens to -1. `Crashed()` reports whether the child died in a
way that indicates a bug, treating the signals used for teardown (SIGTERM,
SIGKILL, SIGINT, SIGHUP) as routine.

### Reading the screen

```go
func (t *Terminal) Screen() Screen
```

`Screen` is an immutable snapshot taken under the terminal's lock:

```go
type Screen interface {
    Size() (cols, rows int)
    Cell(col, row int) Cell
    Cursor() (col, row int, visible bool)
    Text() string
    Line(row int) string
    ExitCode() (code int, exited bool)
}
```

A `Cell` carries `Rune`, `Width` (1 normal, 2 wide, 0 for a wide rune's
continuation column), `Fg` and `Bg` as a `Color` (default, indexed 0-255, or
RGB), and the `Bold`, `Italic`, `Underline`, `Reverse`, `Strikethrough` and
`Blink` attributes.

### Snapshots and golden files

```go
func (t *Terminal) Snapshot() string
func (t *Terminal) SnapshotStyled() string
func (t *Terminal) AssertGolden(tb testing.TB, name string)
func (t *Terminal) AssertGoldenStyled(tb testing.TB, name string)
func Diff(want, got string) string
```

Goldens live in `testdata/<name>.golden`. They are rewritten when `UPDATE_GOLDEN`
is set in the environment, or when the test binary defines a `-update` boolean
flag and it is passed. Diffs are computed in-process with a line LCS, so nothing
shells out to system `diff`.

In the styled encoding a run is `startcol-endcol tokens`, where tokens are a
comma-separated subset of `b` (bold), `i` (italic), `u` (underline), `r`
(reverse), `s` (strikethrough), `k` (blink), `w` (wide rune), `fg:<spec>` and
`bg:<spec>`. A spec is an index `0-255` or `#RRGGBB`. A screen with no styling
degrades to exactly the plain snapshot.

### Lifecycle

```go
func (t *Terminal) ExitCode() (code int, exited bool)
func (t *Terminal) Close() error
```

`Close` is idempotent and is registered automatically by `StartT`.

## The command line

`tuitest` is usable on its own: you can test a TUI, or just look at one, without
writing any Go. Install it with `go install
github.com/Gaurav-Gosain/tuitest/cmd/tuitest@latest`.

```
tuitest <command> [flags] [arguments]

run         play a tape script against a program
record      drive a program by hand and write what you did as a tape
replay      play a tape onto this terminal so you can watch it
snap        spawn a command, wait for it to settle, print the screen
fuzz        drive a program with randomised input and report what breaks
doctor      report on the environment that tests will run in
completion  print a shell completion script
version     print the tuitest version
help        show help for a command
```

Every command has its own help with examples: `tuitest help run`.

### Exit codes

Exit codes are the contract with CI. They separate "your program is wrong" from
"the tool could not run it", so a script can react to each without parsing
stderr.

| Code | Meaning |
| ---- | ------- |
| 0 | every assertion passed |
| 1 | an assertion failed: `Expect`, `Snapshot`, or `ExpectExit` did not hold, or the program exited before a wait was satisfied |
| 2 | bad usage, or a tape that would not parse |
| 3 | harness error: no PTY, a program that would not start, an unreadable golden file |
| 4 | a wait timed out |

### tuitest snap

The fastest way to see what a TUI actually draws. It spawns the program, waits
until it stops drawing, prints the screen, and exits. It asserts nothing, so it
is the natural first step before writing a tape: run it, see the screen, then
pick the text worth waiting for.

```
tuitest snap -- htop
tuitest snap -size 120x40 -- vim
tuitest snap -wait '\$ ' -- bash -i          settle on a prompt instead of on quiet
tuitest snap -type 'hello\r' -- ./myapp      send input first, then capture
tuitest snap -styled -- ./myapp              keep the SGR styling
tuitest snap -json -- ./myapp | jq -r .screen
```

Put `--` before the program so its own flags are not read as tuitest's.

`-type` waits for the program to react before capturing, because the screen has
usually been quiet for longer than the settle window by the time the input is
sent, and a naive capture would show the screen as it was beforehand. When the
response matters, pair it with `-wait`.

### tuitest run

Plays a tape. The tape grammar is unchanged; the flags around it are new.

```
tuitest run login.tape
tuitest run -update login.tape           rewrite the golden files
tuitest run -strict login.tape           reject Sleep, forcing real waits
tuitest run -size 120x40 login.tape      override the tape's Set Size
tuitest run -term screen-256color login.tape
tuitest run -timeout 10s login.tape      override the tape's Set WaitTimeout
tuitest run -env NO_COLOR=1 login.tape   repeatable
tuitest run -golden-dir testdata login.tape
tuitest run -json login.tape             one JSON object, for CI
tuitest run -v login.tape                mirror PTY traffic to stderr
```

A flag beats the tape's own `Set` line for the same setting, which is the
direction that makes `-size 120x40` useful for checking a layout at a second
size without editing the file. `-env` accumulates instead, since environment
entries add up.

### tuitest record

Spawns a program, connects it to your terminal so you can drive it normally, and
writes what you did as a tape. `Ctrl+]` ends the recording.

```
tuitest record -o login.tape -- ./myapp
tuitest record -snapshots -o login.tape -- ./myapp   also write the goldens
tuitest record -cols 80 -rows 24 -o t.tape -- vim    record at a fixed size
tuitest record -- htop                               write the tape to stdout
```

The interesting part is what it does about timing, because that is the
difference between a tape that documents a session and one that is worth
running in CI. Wherever the screen settles, record prefers a `Wait` on text that
is both new and distinctive; if nothing anchorable appeared it falls back to
`WaitStable`; and if the screen never changed at all it emits nothing, since
your thinking time is not part of the test. It never writes a `Sleep` unless you
ask for one with `-idle-sleep`. An anchor that already matched the previous
screen is rejected, because a wait on text that is already there passes
instantly and synchronises nothing.

`-snapshots` captures the screen behind each settle point and writes the golden
files at the same time, so a recording replays green immediately instead of
needing a separate `-update` pass.

A recording is meant to be read and edited. It is written with a header saying
where it came from, and the waits are the part worth skimming before you trust
it as a test.

### tuitest replay

Plays a tape onto your terminal and renders the program as it goes, so you can
watch what a tape does rather than reading its assertions.

```
tuitest replay login.tape
tuitest replay -speed 2 login.tape      run twice as fast
tuitest replay -step login.tape         pause before each command
tuitest replay -update login.tape       rewrite the golden files
```

It wraps the same player `run` uses, so what you watch is what a headless run
does. When an assertion fails it shows the expected and actual screens in two
columns with a `|` against every row that differs, which is easier to read than
a line diff when the difference is a matter of layout.

### tuitest doctor

Reports whether tuitest will work here and whether tests will be stable: PTY
allocation, the platform, `TERM`, the size the program under test will get, what
the bundled emulator understands, and the usual causes of flakiness. It spawns
no program and writes no files, so it is safe as a CI preflight step.

```
tuitest doctor
tuitest doctor -json | jq '.checks[] | select(.status != "ok")'
```

It exits 3 when a check fails, so `tuitest doctor || exit 1` gates a build.

### Machine-readable output

`-json` on `run`, `snap`, and `doctor` prints one object to stdout. `run`
reports the status, the `kind` matching the exit code, the duration, and the
full error text including the screen at the moment of failure:

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

### Error messages

A failure has to be readable without rerunning anything, so each kind of failure
prints what a person actually needs.

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

A parse error names the file, line, and column, and points at the token:

```
tuitest: login.tape:3:11: unexpected token "+Screne" (want /regex/, +Screen, +Line, or @timeout)
  3 | Wait /ok/ +Screne @5s
    |           ^
```

### Shell completion

Completion is generated from the command registry, so it never falls out of step
with the commands themselves.

```
tuitest completion bash > /etc/bash_completion.d/tuitest
tuitest completion zsh  > "${fpath[1]}/_tuitest"
tuitest completion fish > ~/.config/fish/completions/tuitest.fish
```

### The tape language

A tape is line oriented, one command per line, with `#` starting a comment.

```
# Comments start with '#'.
Set Size 40 10
Set Term xterm-256color
Spawn ./myapp
Wait /ready/ +Screen @5s
Type hello
Key Enter
Wait /you said hello/ @5s
Snapshot after-hello +Styled
Type quit
Key Enter
ExpectExit 0
```

Commands: `Set`, `Spawn`, `Type`, `Key`, `Wait`, `WaitStable`, `WaitPrompt`,
`WaitCommand`, `Expect`, `ExpectExit`, `Snapshot`, `Resize`, `Hide`, `Show`,
`Sleep`.

`Set` accepts `Size cols rows`, `Term name`, `Env KEY=VALUE`, `WaitTimeout dur`
and `StabilizeInterval dur`. Durations must be positive and `Size` must be
within 1..10000, since a tape is untrusted input to the CLI. The wait-like
commands take an optional `/regex/`, a `+Screen` or `+Line` scope, and an
`@timeout` such as `@5s`; the pattern runs from the first slash on the line to
the last, so it may contain spaces and slashes. `Wait Stable` is a spelling of
`WaitStable` and takes the same `@timeout`. `Type` preserves the literal
spacing of the rest of its line. `Hide` makes subsequent `Snapshot`
commands no-ops until `Show`, which is how you skip capture during setup steps.
Golden files default to `./testdata`. Under `-strict`, `Sleep` is rejected,
which is a useful way to keep a suite honest.

`Resize cols rows` changes the terminal size mid-tape. `Mouse` takes an action,
a button, a column and a row, plus optional `+Ctrl`, `+Alt`, and `+Shift`, as in
`Mouse Press Left 10 5 +Ctrl`. `Paste` and `Raw` take a Go-quoted string, which
is what lets them carry arbitrary bytes, including malformed UTF-8 and embedded
escape sequences: `Raw "\x1b[1;2;3m"`. `Paste` wraps its text in bracketed-paste
markers; `Raw` writes the bytes exactly as given.

## Fuzzing a TUI

`tuitest fuzz` drives a program with randomised but structured input and reports
the ways a TUI breaks. When it finds something it minimises the input and writes
a tape file that replays it, because a fuzzer that reports a failure without a
reproduction is not actionable.

```
tuitest fuzz -- ./myapp
tuitest fuzz -seed 42 -iterations 200 -corpus testdata/fuzz -- ./myapp
tuitest fuzz -duration 5m -exclude Ctrl+c -- ./myapp
```

### What it sends

Structured input, not byte noise: printable text mixing ASCII, CJK, emoji and
combining marks; navigation, function and control keys; mouse clicks, wheels and
coherent drags, including coordinates outside the grid; bracketed-paste bursts;
and rapid resizes weighted toward degenerate sizes such as one column, one row,
and very large grids.

It also sends deliberately hostile bytes, because a TUI parses its own stdin
looking for key and mouse sequences and that parser sees bytes it did not
produce: truncated and unterminated escape sequences, malformed UTF-8 (bare
continuation bytes, overlong encodings, surrogate halves), enormous parameter
counts, and numeric parameters far past what fits in an integer.

### What it detects

| Finding | What it means |
| --- | --- |
| `crash` | The program died from a fault signal or exited non-zero. |
| `hang` | The program is alive but stopped answering input. |
| `dirty-terminal` | It exited without restoring the terminal. |
| `screen-inconsistent` | The screen model contradicts itself, such as a cursor outside the grid. |
| `memory-growth` | Resident memory grew past `-max-memory-growth` (Linux only, off by default). |

A clean exit is never a finding: the fuzzer sends keys that legitimately quit a
program, and treating that as a bug would make every run a false positive.

`dirty-terminal` is the highest-value check in practice. It is a real bug class,
it is common, and unlike the others it has almost no false-positive surface,
because a program that turned a mode on is unambiguously responsible for turning
it off.

### Reproductions

Every finding is minimised by delta debugging: chunks of input are deleted while
the same failure still occurs, then the survivors are simplified individually.
The result is written as an ordinary tape, so it runs under `tuitest run` with
no fuzz-specific tooling and can be committed as a regression test.

```
# crash: program killed by aborted
# found by tuitest fuzz at seed 13064056694810536104, iteration 6
# minimised from 31 commands to 3
#
# replay with: tuitest run <this file>

Spawn htop
Resize 1 1
Raw "hel"
```

That is a real reproduction, minimised from 31 commands to 3. It is a buffer
overflow in htop 3.5.1, caught by glibc's fortify check.

With `-corpus dir`, findings are saved there and replayed first on the next run,
which turns them into a regression suite: a fix is confirmed when the corpus
stops reproducing. `-seed` makes a session repeatable, and every reported
failure carries the seed that found it.

### Tuning it

`-exclude Ctrl+c,q` stops the fuzzer sending keys that quit the program early.
`-no-hostile`, `-no-mouse` and `-no-resize` narrow the input space.
`-hang-after` sets how long a live program may ignore input before it counts as
hung. `-iterations` and `-duration` bound the run; one of them must be set.

Hang detection is the one heuristic here, and it is deliberately conservative.
A program is allowed to ignore input, so silence alone proves nothing: the check
requires several unanswered input events and then waits the full grace period
for a response. It is tuned to avoid false positives rather than to catch every
hang, so it will miss a wedge whose evidence is muddied by output that was
already in flight.

## How it works

**A real PTY, not a pipe.** The child is started on an actual pseudo-terminal,
so `isatty` is true, `TERM` means something, and the program takes its normal
interactive code path instead of the piped-output one. `Resize` calls
`TIOCSWINSZ`, so the child receives a genuine `SIGWINCH`.

**A VT emulator, not string matching.** Output is fed to a full VT interpreter
that maintains a cell grid: cursor movement, scroll regions, SGR styling,
alternate screen, wide runes. Assertions run against what a user would see after
the escape sequences were applied, so a program that redraws in place or paints
a full-screen UI is testable at all. The emulator is a vendored copy of tuios's
`internal/vt`, behind the small `internal/emu` interface, so it can be swapped.

**Deterministic waits, not sleeps.** Every wait blocks on a condition and is
woken by the output pump the moment new bytes are interpreted, with a short poll
as a backstop for wall-clock conditions. A test therefore runs as fast as the
program does and does not become flaky on a loaded CI machine. `WaitStable`
exists for the cases where "nothing more is coming" is the real condition, and
measures its first quiet window from spawn so it cannot report stability before
the child has produced anything.

**Process-group teardown.** The child is started with `setsid` so it leads its
own session and process group with the PTY as its controlling terminal.
Teardown signals the whole group, `SIGTERM` and then `SIGKILL` after a short
grace period. This is what stops a multiplexer's daemon and its pane processes
from surviving the test; a plain `Process.Kill` would leak every grandchild.

## Status and limitations

The library is complete for the surface documented above, passes under `-race`,
and is exercised against a hermetic Go fixture, a bare `sh`, and the real tuios
binary including a stress test that floods a pane while repeatedly resizing it.

Known limitations, in rough order of how likely you are to hit them:

- **Unix only, loudly.** There is no ConPTY backend, and no process group to
  signal, so teardown could not keep its promise on Windows. The package
  deliberately fails to compile there with a message naming the reason, rather
  than building into something that looks supported and leaks every grandchild
  it spawns. Use WSL or a Unix runner.
- **`Screen.Line` returns one physical row.** A soft-wrapped logical line is not
  de-wrapped, so text that wrapped across the right margin will not match as a
  single string. Match per row, or use `Screen.Text` and account for the wrap.
- **`WaitStable` is a heuristic.** The window is measured from the later of the
  last output and the last input, so it can no longer return the pre-keystroke
  screen, but a program that takes longer than the interval to produce its first
  byte will still be reported stable early. Wait for the expected content with
  `WaitForText`, `WaitForMatch` or `WaitFor` whenever you know it; reach for
  `WaitStable` only after heavy output with no specific end state.
- **The VT emulator is a vendored copy** of tuios's interpreter rather than an
  external dependency, so it does not pick up upstream fixes automatically. The
  exact upstream commit is recorded in `internal/vt/UPSTREAM`, the policy is in
  `internal/vt/VENDOR.md`, and `scripts/vendor-vt.sh -n /path/to/tuios` reports
  drift without changing anything. Fixes go to tuios first; a change made only
  in the copy is lost at the next sync.
- **Emulator throughput is a few thousand lines per second.** A program that
  emits a very large burst will feel PTY backpressure. Size heavy-output tests
  accordingly rather than assuming an instant drain.
- **`SendMouse` only speaks SGR (1006).** The older X10 and UTF-8 mouse
  encodings are not emitted.
- **`tuitest record` captures keys, resizes and timing, not mouse input.** The
  grammar has a `Mouse` verb and the player sends mouse events, but the recorder
  does not yet decode incoming mouse reports back into it. Those reports reach
  the program under test and are then counted and dropped from the tape, and
  recording warns when it happens, since the result is not a complete replay of
  what you did.
- **A recorded wait is only as distinctive as the screen was.** If nothing
  identifiable appeared, the recorder falls back to `WaitStable`, which is
  weaker; skim a recording before trusting it as a test, which is why the output
  is written to be readable and edited.
- **A recorded `Enter` may appear as `Ctrl+j`.** In raw mode a terminal sends
  0x0d for Enter and the recorder names that `Enter`. Some pipes deliver 0x0a
  instead, which is `Ctrl+j`; naming it `Enter` would replay different bytes
  than were recorded, so the literal name is kept.
- **Hang detection is the fuzzer's one heuristic.** There is no universal
  liveness probe for a TUI: no key is guaranteed to produce output, and the
  program does not answer the status queries a terminal would. It is tuned to
  stay quiet, requiring several unanswered inputs and then a full grace period,
  so it will miss a program that wedged while a draw was already in flight.
- **Only crash reproductions carry an assertion.** A crash or a dirty exit ends
  in `ExpectExit 0`, so the file is red until the bug is fixed. A hang, a screen
  inconsistency and memory growth are judged from outside the tape by watching
  the process, and any in-tape liveness probe would mean sending input the
  fuzzer did not send, so those files are transcripts and say so. Rerun
  `tuitest fuzz` against the corpus to check a fix for them.
- **Fuzz generation is blind.** There is no coverage instrumentation of the
  program under test, so input is generated from a structural model rather than
  steered toward new code paths. It finds shallow bugs quickly and deep ones
  only by luck.

## Comparison with the alternatives

**teatest** (`charmbracelet/x/exp/teatest`) drives a Bubble Tea program in
process. That makes it fast and lets it reach into the model, but it only works
for Bubble Tea, and it tests the program rather than the terminal: it does not
run a PTY, so it cannot tell you what a real terminal would show, and it cannot
test anything Bubble Tea did not render. tuitest is the opposite trade. It
treats the binary as a black box behind a real PTY, so it works for any program
in any language and catches escape-sequence and resize behaviour, at the cost of
being slower and having no access to internal state. If you write Bubble Tea and
want fast unit tests of your update loop, use teatest; if you want to know what
the user sees, or you do not control the program's source, use tuitest.

**expect and its descendants** (`expect`, `pexpect`, `go-expect`) also drive a
PTY, and they are excellent at line-oriented conversations: log in, wait for a
prompt, send a password. They match against the byte stream, which is exactly
wrong for a full-screen program, because a TUI's bytes are cursor movements and
partial redraws that never contain the final text in reading order. tuitest
interprets those bytes into a screen first, which is the whole difference. For
scripting an interactive command-line tool, expect is simpler and probably the
better fit. For a program that paints a UI, expect will fight you.

**VHS** records terminal sessions to GIFs and has a tape format that inspired
this one. It is a demo tool, not an assertion tool. tuitest's tape language
covers the harness primitives and produces golden text, not video.

## Driving tuios

`tuiosx` holds the tuios-specific conveniences: a `Prefix` leader-chord helper
and a `StartTuios` spawn helper that isolates each instance in its own temporary
XDG directories, so parallel tests never collide on a shared daemon socket. It
is entirely optional and nothing in the core depends on it.

The acceptance tests there are gated on an environment variable, because they
spawn a full multiplexer:

```
TUITEST_TUIOS=1 go test -race ./tuiosx/...
```

The examples under `examples/tuios` skip themselves unless a tuios binary is
found through `TUIOS_BIN` or `PATH`. They are worth reading as realistic usage
even if you never run them: they cover boot and window management, a control
plane driven over a unix socket with a TUI later attached to the same session,
and a flood-plus-resize stress test.

## Running the tests

```
go build ./...
go vet ./...
go test -race ./...
```

The default suite is hermetic: it spawns a small Go echo-TUI fixture under
`testdata/echotui` and a plain `sh`, and requires no tuios.

Everything that parses input tuitest does not control has a fuzz target: the
tape lexer and parser, the key-name resolver, the golden diff, the styled
snapshot encoder, and the emulator behind the screen accessors. Their seed
corpora live in `testdata/fuzz`, so `go test` runs them as ordinary unit tests
and they act as regression guards with no fuzzing session. To actually fuzz:

```
go test -run '^$' -fuzz FuzzParse ./tape
go test -run '^$' -fuzz FuzzEmulatorScreen .
```

Set `TUITEST_TUIOS_SRC` to a tuios checkout to have the suite also verify that
the vendored emulator still matches the commit recorded in
`internal/vt/UPSTREAM`.

## License

MIT. See [LICENSE](LICENSE).

The vendored VT emulator under `internal/vt` is copied from tuios, which is also
MIT licensed by the same author.
