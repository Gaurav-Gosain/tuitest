# The Go API

Everything here is in the root package, `github.com/Gaurav-Gosain/tuitest`. The
generated reference is on
[pkg.go.dev](https://pkg.go.dev/github.com/Gaurav-Gosain/tuitest); this page is
the narrative version, covering what each group is for and which of two similar
calls to reach for.

## Spawning

```go
func Start(argv []string, opts ...Option) (*Terminal, error)
func StartT(tb testing.TB, argv []string, opts ...Option) *Terminal
```

`Start` is the plain constructor. `StartT` is the one to use under `go test`: it
wires the debug log to `t.Log`, registers `Close` through `t.Cleanup`, and calls
`t.Fatalf` if the spawn itself fails.

| Option | Effect |
| --- | --- |
| `WithSize(cols, rows int)` | Initial PTY size. Default 80x24. |
| `WithEnv(kv ...string)` | Add or override `KEY=VALUE` entries. |
| `WithInheritEnv()` | Start from the parent environment instead of the hermetic default. |
| `WithDir(path string)` | Child working directory. |
| `WithTerm(term string)` | `TERM` value. Default `xterm-256color`. |
| `WithTrueColor()` | Sets `COLORTERM=truecolor`. |
| `WithLog(w io.Writer)` | Mirror PTY I/O in both directions to `w`. |
| `WithOutputMirror(w io.Writer)` | Copy only what the program wrote to `w`, as it arrives. |
| `WithSemanticMarkers()` | Enable the OSC 133 waits. |
| `WithStabilizeInterval(d time.Duration)` | Quiet window for `WaitStable`. Default 150ms. |

By default the child gets a minimal hermetic environment: `PATH` and `HOME` if
the parent has them, `LANG=C.UTF-8`, and `TERM`. A developer's shell
configuration therefore cannot change the result. Later entries win over
earlier ones, so `WithEnv` overrides everything the defaults set.

`WithLog` and `WithOutputMirror` differ in direction and purpose. `WithLog` is
for debugging and carries input as well as output; `WithOutputMirror` carries
only the program's own bytes, so `w` can be a real terminal the program is
rendered onto while the harness drives it headlessly. `tuitest record` and
`tuitest replay` are built on the second.

## Input

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
`Down`, `Left`, `Right`, `Home`, `End`, `PageUp`, `PageDown`, `Insert`, and `F1`
through `F12`. `Ctrl(r rune)` builds a control byte (`Ctrl('b')` is 0x02, and
letters are case-insensitive) and `Alt(k any)` prefixes with ESC. `Ctrl` follows
the xterm table for the other keys too: `Ctrl(' ')` and `Ctrl('2')` are NUL,
`Ctrl('[')` is ESC, `Ctrl('~')` is 0x1e, `Ctrl('/')` is 0x1f, `Ctrl('?')` is
DEL, and a rune with no control encoding is
sent as itself.

`SendKeys` sends the arrow keys, `Home` and `End` the way a terminal does in the
program's current cursor key mode: `ESC [ A` normally, and `ESC O A` once the
program has set DECCKM (mode 1). Full-screen programs built on terminfo set that
mode at startup and match their input against the second form, so a harness
that sent the first regardless would be pressing keys the program does not
know.

Sending input to a program that has exited returns an error wrapping
`ErrChildExited`, the same sentinel a wait uses, with the exit code in its
message. Input that races the exit can fail before the exit is recorded (macOS
refuses writes to a PTY whose program has closed its end), and that error wraps
`ErrChildExited` too, with `ExitStatus` already reporting the exit when it
arrives. Linux accepts such a write and discards the bytes.

`SendMouse` emits SGR (mode 1006) sequences with 1-based wire coordinates from
the zero-based `Col`/`Row` you give it. It writes them unconditionally, so a
program that never enabled mouse reporting will not react at all.

Motion comes in two spellings and they are not interchangeable. `MouseMove` is
motion with nothing held, and it drops whatever `Button` you set; `MouseDrag` is
motion with a button held and requires one. A drag written as `MouseMove` with a
button still encodes as no button, so a program tracking mode 1002 receives
nothing between the press and the release and the test quietly exercises
any-motion reporting instead.

`Paste` wraps text in bracketed-paste markers (mode 2004), the way a terminal
delivers a real paste. Programs take a different code path for pasted text than
for typed text, and it is usually the less tested one. The markers are sent
whether or not the program enabled the mode; a real terminal sends a paste to a
program that did not as plain text, which is what `Type` does.

`Resize` changes the PTY window size, so the child receives a real `SIGWINCH`,
and resizes the emulator grid in the same call. A program that enabled in-band
resize reports (mode 2048) also gets its report straight away. A width or
height outside 1..65535 is refused with an error and changes nothing, since the
kernel cannot store it; `WithSize` has the same range, and `Start` refuses
anything outside it.

## Waiting

```go
func (t *Terminal) WaitForText(substr string, timeout time.Duration) error
func (t *Terminal) WaitForMatch(re *regexp.Regexp, scope Scope, timeout time.Duration) error
func (t *Terminal) WaitFor(cond func(Screen) bool, timeout time.Duration) error
func (t *Terminal) WaitForStable(cond func(Screen) bool, timeout time.Duration) error
func (t *Terminal) WaitForOutput(timeout time.Duration) error
func (t *Terminal) WaitStable(timeout time.Duration) error
func (t *Terminal) WaitExit(timeout time.Duration) (int, error)
func (t *Terminal) Done() <-chan struct{}
```

`Scope` is `ScopeScreen` (the whole screen) or `ScopeLastLine` (the last
non-blank row, useful for prompts). A timeout of zero or less is treated as one
second.

`WaitStable` and `WaitForOutput` answer different questions and are easy to
confuse. `WaitStable` waits for output to go quiet, which is what you want after
a burst. `WaitForOutput` waits for the program to write something after the call
begins, which is what you want after sending input: once the screen has settled,
`WaitStable` is already satisfied and returns without the program having done
anything at all.

`WaitStable` measures its quiet window from the later of the last output byte
and the last input tuitest sent, and does not settle at all before the child's
first byte, so it can neither report the pre-keystroke screen as stable nor
return a blank screen from a program that is slow to start. It remains a
heuristic: a program that takes longer than the interval to react to input is
reported stable too early. Wait for the content you expect whenever you know it.

`WaitForStable` is `WaitFor` plus a settle: it returns once the condition holds
on a screen whose cells have not changed for the stabilize interval. A program
draws a frame from the top down and the PTY hands it over in pieces, so the
moment the text a test waits for appears, the rows below it can still hold the
previous frame. Use it when the assertion that follows reads more of the screen
than the condition checked, and to read a value that is still moving, such as a
count or a scroll position, once it has stopped:

```go
// The overlay's first row is on screen; wait for the rest of it too.
err := term.WaitForStable(func(s tuitest.Screen) bool {
    return strings.Contains(s.Text(), "Toggle tiling")
}, 5*time.Second)
```

Output that repaints the same content does not restart its window, and neither
does cursor movement.

### Synchronized output

A program that wraps each frame in a synchronized update (DEC mode 2026) is
asking the terminal not to show the frame until it is complete. Bubble Tea v2
does this whenever the terminal says it supports the mode, and tuitest's
terminal does. While an update is open, every read of the screen, the waits
included, sees the last complete frame, exactly as on a real terminal. An update
left open for more than a second is shown as it stands, and so is everything
once the program exits.

`WaitExit` waits for process exit rather than screen state. `Done` returns a
channel closed after the child is reaped, which lets a caller select on program
exit alongside its own events. `Wait` is a deprecated alias for `WaitExit`; it
read as a sibling of the `WaitFor` family when in fact it waits on the process.

### Failure

A wait that times out returns `*TimeoutError`; a wait whose child exits first
returns `*ClosedError`. Both embed the screen at the moment of failure and the
tail of the mirrored I/O, so a CI failure reads like this rather than as a bare
`timeout`:

```
tuitest: WaitForText timed out after 3s waiting for text "you said hello"
--- screen ---
myapp v0.2
ready
> hello
--- last I/O ---
...
```

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

### Semantic (OSC 133) waits

```go
func (t *Terminal) WaitForPrompt(timeout time.Duration) error
func (t *Terminal) WaitForCommand(timeout time.Duration) error
func (t *Terminal) LastCommandExit() (code int, ok bool)
```

These synchronize on the shell's own prompt-start (OSC 133 A) and
command-finished (OSC 133 D) markers, which is more reliable than matching
prompt text: prompt text is user-configurable and often contains the same
substrings the command output does. They require `WithSemanticMarkers`, and
return an error wrapping `ErrSemanticMarkers` without it rather than silently
never firing. `LastCommandExit` reports `false` both when no command has
finished and when the option was not passed, so prefer the waits as the signal.

## Terminal state and exit status

```go
func (t *Terminal) TermState() TermState
func (t *Terminal) ExitStatus() (ExitStatus, bool)
func (t *Terminal) ExitCode() (code int, exited bool)
func (t *Terminal) Progress() (bytes int64, last time.Time)
func (t *Terminal) Pid() int
```

`TermState` reports the modes a program has left set: the alternate screen
(47, 1047 or 1049), mouse tracking (9, 1000, 1001, 1002, 1003), bracketed paste
(2004), focus reporting (1004), and cursor visibility. `Mode(n int)` answers for
any DEC private mode number. Called after the child exits, `Dirty()` answers
"did this program restore the terminal?", which is a common and user-visible
bug: a TUI that exits without leaving the alternate screen, or with mouse
reporting still on, leaves the user's shell unusable. `Describe()` names the
offending modes in a stable order for an error message.

`ExitStatus` separates a signal death from an ordinary non-zero exit, which
`ExitCode` alone flattens to -1. `Crashed()` reports whether the child died in a
way that indicates a bug, treating SIGTERM, SIGKILL, SIGINT, SIGHUP and SIGPIPE
as routine teardown or hangup rather than evidence.

`Progress` reports how many bytes the child has written and when the most recent
write landed. A caller that sends input and then sees neither counter move has
evidence the program stopped responding; this is the primitive the fuzzer's hang
detector is built on.

## Reading the screen

```go
func (t *Terminal) Screen() Screen
func Find(s Screen, substr string) (col, row int, ok bool)

type Screen interface {
    Size() (cols, rows int)
    Cell(col, row int) Cell
    Cursor() (col, row int, visible bool)
    Text() string
    Line(row int) string
    ExitCode() (code int, exited bool)
}
```

`Screen` is an immutable snapshot taken under the terminal's lock, so a
condition callback may stash it without observing a torn write from the output
pump. It is reused until the grid changes, so reading it in a loop is cheap.

`Find` returns the cell column and row where a string starts, which is the
coordinate `SendMouse` takes. It is not the offset of the string in `Line`: a
box-drawing border is three bytes in one cell and a CJK character is one rune in
two cells, so an offset computed from the text lands beside the target. `Text` joins the rows with trailing blanks trimmed per line and trailing
blank lines dropped. `Line` returns one physical row and does not de-wrap: a
logical line that soft-wrapped at the right margin occupies several rows and
will not match as one string.

A `Cell` carries `Content`, `Rune`, `Width` (1 normal, 2 wide, 0 for a wide
rune's continuation column), `Fg` and `Bg` as a `Color` (`ColorDefault`,
`ColorIndexed` with an `Index`, or `ColorRGB` with `R`, `G`, `B`), and the
`Bold`, `Faint`, `Italic`, `Underline`, `Reverse`, `Strikethrough`, `Blink`
and `Conceal` attributes. A concealed cell renders as blanks in `Line` and
`Text`, as it does on screen. `Content` is the whole grapheme cluster, base rune plus any
combining marks, joiners and modifiers, and is what `Line` and `Text` render
and what a caller should match against. `Rune` is its first rune, so a cell
holding `e` plus a combining acute reports `e` there.

## Snapshots and golden files

```go
func (t *Terminal) Snapshot() string
func (t *Terminal) SnapshotStyled() string
func (t *Terminal) AssertGolden(tb testing.TB, name string)
func (t *Terminal) AssertGoldenStyled(tb testing.TB, name string)
func Diff(want, got string) string
```

Goldens live in `testdata/<name>.golden`. They are rewritten when
`UPDATE_GOLDEN` is set in the environment, or when the test binary defines an
`-update` boolean flag and it is passed. A trailing newline and CRLF line
endings in a golden file are ignored, so one saved by an editor or checked out
on Windows still matches. Diffs are computed in-process with a
line LCS, so nothing shells out to system `diff`. `Diff` is exported so
out-of-package golden runners, such as the tape player, can reuse the same
encoding.

In the styled encoding a run is `startcol-endcol tokens`, where tokens are a
comma-separated subset of `b` (bold), `f` (faint), `c` (concealed), `i`
(italic), `u` (underline), `r` (reverse), `s` (strikethrough), `k` (blink), `w`
(wide rune), `fg:<spec>` and
`bg:<spec>`. A spec is an index `0-255` or `#RRGGBB`. A screen with no styling
degrades to exactly the plain snapshot.

```
 Preferences
    1-11 b,fg:15,bg:4
 Theme:  dark
    9-12 fg:2
```

## Lifecycle

```go
func (t *Terminal) Close() error
```

`Close` tears down the whole process group, any descendant that left it with
`setsid`, and the PTY. It sends SIGTERM, then SIGKILL after two seconds to
anything still running. It returns an error naming any process that survived
both, which `StartT`'s cleanup reports as a test failure. It is idempotent and
is registered automatically by `StartT`. See [limits.md](limits.md#teardown)
for what it cannot reach.

## A worked example

Driving a program with keyboard and mouse, resizing it mid-flight, waiting for
the redraw to settle, and pinning the styled result:

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

    term.AssertGoldenStyled(t, "prefs-60x20")
}
```

Run it once with `UPDATE_GOLDEN=1 go test ./...` to record
`testdata/prefs-60x20.golden`, then review that file as part of the diff.

## Driving tuios

`tuiosx` holds the tuios-specific conveniences: `Prefix` sends the leader chord
(Ctrl+B) followed by a key, `Locate` finds the binary through `TUIOS_BIN` or
`PATH`, and `StartTuios` spawns an instance with its own temporary XDG
directories so parallel tests never collide on a shared daemon socket. It is 69
lines and entirely optional; nothing in the core depends on it, and it can be
deleted without touching the harness.
