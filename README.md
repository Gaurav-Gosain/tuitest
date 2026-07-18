# tuitest

tuitest is a headless testing harness for terminal programs. It drives a program
under test through a real pseudo-terminal, interprets the output with a VT
emulator, and lets tests assert on the resulting screen as a grid of cells rather
than as a raw byte stream. Its first target is tuios, but the core library has no
tuios-specific knowledge and works against any TUI, a shell, vim, or a bubbletea
app.

## Requirements

- Go 1.25 or newer.
- A Unix-like OS that can open PTYs (`/dev/ptmx`). Linux is the CI target.
  Windows is not supported in this version.

## Library usage

Spawn a program, drive input, wait on screen state, and assert.

```go
func TestGreeting(t *testing.T) {
    term := tuitest.StartT(t, []string{"./myapp"}, tuitest.WithSize(80, 24))

    if err := term.WaitForText("ready", 5*time.Second); err != nil {
        t.Fatal(err)
    }
    term.SendKeys("hello", tuitest.Enter)
    if err := term.WaitForText("you said hello", 3*time.Second); err != nil {
        t.Fatal(err)
    }
    term.AssertGolden(t, "after-hello")
}
```

`StartT` wires the PTY debug log to `t.Log`, registers teardown via `t.Cleanup`,
and fails the test on a spawn error. `Start` is the plain constructor for use
outside `go test`.

### Driving input

- `SendKeys(items ...any)` accepts strings, runes, and `Key` values.
  Plain text is sent literally; `Key` values carry their own escape sequences.
  Example: `term.SendKeys(tuitest.Ctrl('b'), "%")`.
- `Type(s string)` sends literal text with no key-name interpretation.
- `SendMouse(ev MouseEvent)` sends an SGR mouse event.
- `Resize(cols, rows int)` resizes the PTY and delivers SIGWINCH to the child.

Named keys are typed values (`tuitest.Enter`, `tuitest.Up`, `tuitest.F5`), so a
mistyped name is a compile error. `Ctrl(r rune)` and `Alt(k any)` build chords.

### Waiting

No test sleeps to synchronize. Every wait is a condition or a stabilization with
a timeout, and every timeout error carries a full screen dump plus the tail of
the mirrored I/O log.

- `WaitForText(substr, timeout)`
- `WaitForMatch(re, scope, timeout)` with `ScopeScreen` or `ScopeLastLine`
- `WaitFor(func(Screen) bool, timeout)` for arbitrary conditions
- `WaitStable(timeout)` blocks until output has been quiet for the configured
  stabilization window

`WaitStable` proves only that output stopped, not that a specific change
happened. When a keystroke triggers a delayed reaction (a subprocess spawning, a
window opening), wait for the expected content with `WaitForText` or `WaitFor`
instead, because the quiet window can elapse before the reaction lands.

With `WithSemanticMarkers`, OSC 133 shell integration enables `WaitForPrompt`,
`WaitForCommand`, and `LastCommandExit`.

### Snapshots and golden files

- `Snapshot()` returns the plain-text screen.
- `SnapshotStyled()` returns a diff-friendly per-row attribute-run encoding.
- `AssertGolden(tb, name)` and `AssertGoldenStyled(tb, name)` compare against
  `testdata/<name>.golden`, failing with an in-process unified diff.

Golden files are rewritten when the `UPDATE_GOLDEN` environment variable is set,
or when a `-update` flag is defined and passed. Multiple named snapshots per test
are supported.

The styled encoding emits each row's plain text followed by indented attribute
runs for spans that differ from the default style. A run is `startcol-endcol
tokens`, where tokens are a comma-separated subset of `b` (bold), `i` (italic),
`u` (underline), `r` (reverse), `s` (strike), `k` (blink), `w` (wide rune),
`fg:<spec>`, and `bg:<spec>`. A color spec is `default`, an index `0-255`, or
`#RRGGBB`. A screen with no styling degrades to the plain snapshot.

### Environment

By default the child starts from a minimal hermetic environment (`PATH`, `HOME`,
`TERM`, `LANG=C.UTF-8`). `WithEnv` adds or overrides entries, `WithInheritEnv`
opts into the parent environment, `WithTerm` overrides `TERM` (default
`xterm-256color`), and `WithTrueColor` sets `COLORTERM=truecolor`.

## CLI usage

The `tuitest` command runs tape scripts so non-Go users can write tests. The
grammar is a small VHS-inspired subset.

```
tuitest run script.tape
tuitest run -update script.tape     # rewrite golden snapshots
tuitest run -strict script.tape     # treat Sleep as an error
tuitest run -v script.tape          # mirror PTY I/O to stderr
```

An example tape:

```
Set Size 40 10
Spawn ./myapp
Wait /ready/ +Screen @5s
Type hello
Key Enter
Wait /you said hello/ +Screen @5s
Snapshot after-hello
Type quit
Key Enter
ExpectExit 0
```

Commands: `Set`, `Spawn`, `Type`, `Key`, `Wait`, `WaitStable`, `WaitPrompt`,
`WaitCommand`, `Expect`, `ExpectExit`, `Snapshot` (with optional `+Styled`),
`Hide`, `Show`, and `Sleep`. `Wait` and `Expect` take an optional `/regex/`, a
`+Screen` or `+Line` scope, and an `@timeout`. Lines beginning with `#` are
comments. `Hide` and `Show` bracket steps whose output should not land in a
snapshot.

## Driving tuios

tuitest spawns the compiled tuios binary directly inside its harness PTY and
treats it as a black box, which exercises the whole stack including the window
manager. The `tuiosx` package adds a `Prefix` chord helper and a `StartTuios`
spawn helper that isolates each instance in its own temporary XDG directories, so
parallel tests do not collide on a shared daemon socket and the process-group
teardown reaps the daemon and pane processes on `Close`.

The tuios acceptance tests in `tuiosx` are gated behind the `TUITEST_TUIOS`
environment variable:

```
TUITEST_TUIOS=1 go test -race ./tuiosx/...
```

## Running the tests

```
go build ./...
go test -race ./...
```

The default suite spawns a small Go echo-TUI fixture (`testdata/echotui`) and a
plain `sh`, so it is hermetic and does not require tuios. The tuios acceptance
tests are skipped unless `TUITEST_TUIOS` is set.

## Status

Working:

- PTY spawn and controlled environment, output pump, resize, exit-code
  collection, and process-group teardown (SIGTERM then SIGKILL to the group).
- Cell-grid screen model over tuios's VT emulator, with plain and styled text.
- `SendKeys`, `Type`, named keys and chords, SGR mouse encoding.
- `WaitForText`, `WaitForMatch`, `WaitFor`, `WaitStable`, and the semantic
  waits, all with screen-dumping timeout errors.
- Plain and styled snapshots with `UPDATE_GOLDEN` golden files.
- The tape parser, player, and `tuitest run` CLI.
- The race detector passes (`go test -race ./...`).

Limitations:

- Unix only. There is no ConPTY backend yet.
- `Screen.Line` returns a single physical row; soft-wrapped continuation lines
  are not joined into one logical line.
- The tuios acceptance tests cover boot and window creation. Deeper pane-input
  scenarios depend on tuios's modal focus behavior and are left to tuios's own
  suite; the deterministic heavy-output stress runs against `sh` instead.
- Emulator throughput is a few thousand lines per second, so a program that
  emits a very large burst applies PTY backpressure. Size heavy-output tests
  accordingly rather than assuming instant drain.

## Decisions

Where the design document and reality diverged, the calls made here are:

1. Module path. The design suggested `github.com/gauravgosain/tuitest`. This
   module uses `github.com/Gaurav-Gosain/tuitest` to match the existing tuios
   repository's organization casing.

2. VT emulator dependency. The design's preferred option was to depend directly
   on `charmbracelet/ultraviolet` as the emulator. That is not possible:
   ultraviolet provides the cell, style, and screen-buffer types and an output
   renderer, but it does not provide a byte-stream VT interpreter. The actual
   emulator that parses a PTY stream into a cell grid is tuios's `internal/vt`,
   which sits on top of ultraviolet and imports only ultraviolet and
   `charmbracelet/x/ansi`. This module therefore takes the design's fallback
   option in full: it copies tuios's `internal/vt` package verbatim (non-test
   files only) into `internal/vt`, behind the `internal/emu` adapter interface.
   This maximizes fidelity with tuios (the identical interpreter) and provides
   OSC 133 semantic markers without extra work. The emulator remains swappable
   behind the `internal/emu` interface.

3. Semantic markers are always tracked by the copied emulator. The
   `WithSemanticMarkers` option gates only the public wait API, so tests opt in
   explicitly.

4. `WaitStable` measures its first quiet window from spawn time, so it does not
   report "stable" before the child has produced any output.

5. The SGR mouse encoder builds sequences directly rather than consulting the
   emulator's reported mouse mode, so `SendMouse` works regardless of what the
   child has requested. Programs that ignore SGR mouse reporting will not react.

6. Golden diffs are computed in-process (a small line LCS) to stay hermetic; no
   system `diff` is invoked.
