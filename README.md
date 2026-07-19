# tuitest

tuitest is a headless testing harness for terminal programs. It runs the program
under test in a real pseudo-terminal, interprets its output with a VT emulator,
and lets you assert on the resulting screen as a grid of cells rather than as a
raw byte stream.

It is for anyone who has a terminal program and wants to test what a user would
actually see: TUI authors, shell tooling authors, and people maintaining
terminal multiplexers. It has no knowledge of any particular UI framework, so it
works equally against a Bubble Tea app, a Rust or C TUI, vim, or a bare shell.

It also fuzzes. `tuitest fuzz` points randomised but structured input at any
terminal program and hunts for crashes, hangs, and terminals left in a broken
state, then minimises whatever it finds into a tape file that replays it. See
[Fuzzing a TUI](#fuzzing-a-tui).

## Requirements

- Go 1.25 or newer.
- A Unix-like OS that can open PTYs (`/dev/ptmx`). Windows is not supported; see
  Limitations.

## Installation

```
go get github.com/Gaurav-Gosain/tuitest
```

The tape CLI, if you want it:

```
go install github.com/Gaurav-Gosain/tuitest/cmd/tuitest@latest
```

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
func (t *Terminal) WaitForOutput(timeout time.Duration) error
func (t *Terminal) Wait(timeout time.Duration) (int, error)
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

## The tape CLI

`cmd/tuitest` runs a line-oriented, VHS-inspired script so tests can be written
without Go. It is a thin front end over the same API and adds no capability the
library lacks.

```
tuitest run script.tape
tuitest run -update script.tape      # rewrite golden snapshots
tuitest run -strict script.tape      # make Sleep an error
tuitest run -v script.tape           # mirror PTY I/O to stderr
tuitest run -golden-dir dir script.tape
```

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

Commands: `Set`, `Spawn`, `Type`, `Key`, `Wait`, `WaitStable`, `WaitOutput`,
`WaitPrompt`, `WaitCommand`, `Expect`, `ExpectExit`, `Snapshot`, `Resize`,
`Mouse`, `Paste`, `Raw`, `Hide`, `Show`, `Sleep`.

`Set` accepts `Size cols rows`, `Term name`, `Env KEY=VALUE`, `WaitTimeout dur`
and `StabilizeInterval dur`. The wait-like commands take an optional `/regex/`,
a `+Screen` or `+Line` scope, and an `@timeout` such as `@5s`. `Type` preserves
the literal spacing of the rest of its line. `Hide` makes subsequent `Snapshot`
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

- **Unix only.** There is no ConPTY backend. The Windows files are stubs that
  keep the package compiling; process-group teardown does nothing there.
- **`Screen.Line` returns one physical row.** A soft-wrapped logical line is not
  de-wrapped, so text that wrapped across the right margin will not match as a
  single string. Match per row, or use `Screen.Text` and account for the wrap.
- **`WaitStable` can observe a pre-action frame.** Called immediately after
  sending input, the quiet window can elapse before the program has reacted, and
  it will report stability against the old screen. Wait for the expected content
  with `WaitForText`, `WaitForMatch` or `WaitFor`, or for any reaction at all
  with `WaitForOutput`; reach for `WaitStable` only after heavy output where no
  specific end state is known.
- **The VT emulator is a vendored copy** of tuios's interpreter rather than an
  external dependency, so it does not pick up upstream fixes automatically.
- **Emulator throughput is a few thousand lines per second.** A program that
  emits a very large burst will feel PTY backpressure. Size heavy-output tests
  accordingly rather than assuming an instant drain.
- **`SendMouse` only speaks SGR (1006).** The older X10 and UTF-8 mouse
  encodings are not emitted.
- **Fuzzing cannot see inside the program.** Crashes, exit status, and terminal
  state are observed from outside, so there is no coverage guidance and no
  goroutine accounting; memory growth is read from `/proc` and is Linux only.
  Hang detection is a heuristic tuned to avoid false positives, so it misses
  some real hangs.

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

The fuzzer is tested against `testdata/buggytui`, a fixture with deliberate,
individually selectable bugs: one that panics on a specific key, one that wedges
when resized to a single column, and one that exits without restoring the
terminal. The tests assert that the fuzzer finds each of them and minimises a
tape that reproduces it. Just as importantly, the same fixture has a `none` mode
that is well behaved, and a test asserts that the detectors stay silent on it
across several seeds; that control is what keeps the fuzzer from becoming noise.

## License

MIT. See [LICENSE](LICENSE).

The vendored VT emulator under `internal/vt` is copied from tuios, which is also
MIT licensed by the same author.
