# tuitest

tuitest is a headless testing harness for terminal programs. It runs the program
under test in a real pseudo-terminal, interprets its output with a VT emulator,
and lets you assert on the resulting screen as a grid of cells rather than as a
raw byte stream.

It is for anyone who has a terminal program and wants to test what a user would
actually see: TUI authors, shell tooling authors, and people maintaining
terminal multiplexers. It has no knowledge of any particular UI framework, so it
works equally against a Bubble Tea app, a Rust or C TUI, vim, or a bare shell.

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

### Waiting

```go
func (t *Terminal) WaitForText(substr string, timeout time.Duration) error
func (t *Terminal) WaitForMatch(re *regexp.Regexp, scope Scope, timeout time.Duration) error
func (t *Terminal) WaitFor(cond func(Screen) bool, timeout time.Duration) error
func (t *Terminal) WaitStable(timeout time.Duration) error
func (t *Terminal) Wait(timeout time.Duration) (int, error)
```

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

tuitest record -o script.tape ./myapp  # record a session into a tape
tuitest replay script.tape             # watch a tape run
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

Commands: `Set`, `Spawn`, `Type`, `Key`, `Wait`, `WaitStable`, `WaitPrompt`,
`WaitCommand`, `Expect`, `ExpectExit`, `Snapshot`, `Resize`, `Hide`, `Show`,
`Sleep`.

`Set` accepts `Size cols rows`, `Term name`, `Env KEY=VALUE`, `WaitTimeout dur`
and `StabilizeInterval dur`. The wait-like commands take an optional `/regex/`,
a `+Screen` or `+Line` scope, and an `@timeout` such as `@5s`. `Type` preserves
the literal spacing of the rest of its line. `Resize cols rows` changes the
window mid-run, so the child gets a real `SIGWINCH`. `Hide` makes subsequent
`Snapshot` commands no-ops until `Show`, which is how you skip capture during
setup steps. Golden files default to `./testdata`. Under `-strict`, `Sleep` is
rejected, which is a useful way to keep a suite honest.

## Recording a tape

Writing tapes by hand gets tedious, so `tuitest record` writes one for you. It
spawns the program on a PTY, passes your real terminal through to it so you
interact normally, and captures what you did.

```
tuitest record -o login.tape ./myapp
tuitest record -o login.tape -snapshots ./myapp   # also write goldens
```

Press `Ctrl+]` to stop. The result is a tape a human would want to edit, not a
keystroke dump: printable runs coalesce into `Type` commands, everything else
becomes named `Key` tokens, and terminal resizes become `Resize`.

Timing is the interesting part, because a recording full of fixed sleeps is
exactly the brittle test this harness exists to avoid. At each point where the
screen stopped changing, the recorder picks the strongest synchronization it
can justify:

- if distinctive new text appeared, it waits on that text, so the tape survives
  the program being slower or faster on replay;
- if the screen changed but nothing anchorable appeared, it emits `WaitStable`;
- if the screen never changed, it emits nothing, because your think-time is not
  part of the test.

`Sleep` is only emitted if you opt in with `-idle-sleep`, for programs whose
behavior genuinely depends on wall-clock delay.

With `-snapshots`, a `Snapshot` is taken at every settle point and its golden is
written at the same time, so the recording doubles as golden generation and
replays green immediately.

## Replaying a tape

`tuitest replay` plays a tape onto your terminal in real time, so a failing test
can be watched rather than deduced.

```
tuitest replay script.tape
tuitest replay -speed 2 script.tape    # twice as fast
tuitest replay -step script.tape       # pause before each command
```

Each command is printed as it runs, and the program is rendered live. When an
assertion fails, replay shows the frame it failed on: a golden mismatch is
printed as the expected and actual screens side by side, with a `|` marking each
row that differs.

```
FAIL snapshot "banner" (testdata/banner.golden)

expected (golden)          actual (screen)
------------------------   ------------------------
ECHOTUI                    ECHOTUI
> WRONG LINE HERE        | >
```

Recording and replay close the loop: record a session, keep the tape as a test,
and replay it when it breaks.

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
  with `WaitForText`, `WaitForMatch` or `WaitFor` instead; reach for `WaitStable`
  only after heavy output where no specific end state is known.
- **The VT emulator is a vendored copy** of tuios's interpreter rather than an
  external dependency, so it does not pick up upstream fixes automatically.
- **Emulator throughput is a few thousand lines per second.** A program that
  emits a very large burst will feel PTY backpressure. Size heavy-output tests
  accordingly rather than assuming an instant drain.
- **`SendMouse` only speaks SGR (1006).** The older X10 and UTF-8 mouse
  encodings are not emitted.
- **`tuitest record` captures keys, resizes and timing, not mouse input.** The
  tape grammar has no mouse verb, so mouse reports and other sequences with no
  tape equivalent are dropped from a recording. Recording warns when this
  happens, since the resulting tape is then not a complete replay of what you
  did.
- **A recorded wait is only as distinctive as the screen was.** If nothing
  identifiable appeared, the recorder falls back to `WaitStable`, which is
  weaker; skim a recording before trusting it as a test, which is why the output
  is written to be readable and edited.

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

## License

MIT. See [LICENSE](LICENSE).

The vendored VT emulator under `internal/vt` is copied from tuios, which is also
MIT licensed by the same author.
