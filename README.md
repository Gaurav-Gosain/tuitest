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

The command-line tool, which tests a TUI without any Go:

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

## The command line

`tuitest` is usable on its own: you can test a TUI, or just look at one, without
writing any Go. Install it with `go install
github.com/Gaurav-Gosain/tuitest/cmd/tuitest@latest`.

```
tuitest <command> [flags] [arguments]

run         play a tape script against a program
snap        spawn a command, wait for it to settle, print the screen
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
`WaitCommand`, `Expect`, `ExpectExit`, `Snapshot`, `Hide`, `Show`, `Sleep`.

`Set` accepts `Size cols rows`, `Term name`, `Env KEY=VALUE`, `WaitTimeout dur`
and `StabilizeInterval dur`. The wait-like commands take an optional `/regex/`,
a `+Screen` or `+Line` scope, and an `@timeout` such as `@5s`. `Type` preserves
the literal spacing of the rest of its line. `Hide` makes subsequent `Snapshot`
commands no-ops until `Show`, which is how you skip capture during setup steps.
Golden files default to `./testdata`. Under `-strict`, `Sleep` is rejected,
which is a useful way to keep a suite honest.

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
