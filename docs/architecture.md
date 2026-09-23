# Architecture

tuitest is one public package and a handful of internal or auxiliary ones. Each
has a single responsibility and a narrow seam to the next, so any one of them
can be read, tested, or replaced without the others.

## Packages

| Package | Responsibility |
| --- | --- |
| [`.`](../terminal.go) (root) | `Terminal`: spawn options, input, waits, screen snapshots, goldens, terminal state |
| [`internal/ptyproc`](../internal/ptyproc/ptyproc.go) | PTY allocation, `exec` lifetime, the output pump, resize, process-group teardown |
| [`internal/emu`](../internal/emu/emu.go) | The `Emulator` interface and the adapter onto the vendored VT |
| [`internal/vt`](../internal/vt) | The VT interpreter itself, copied from tuios (see [VENDOR.md](../internal/vt/VENDOR.md)) |
| [`tape`](../tape/parse.go) | The tape language: parser, player, recorder, printer, replay renderer |
| [`internal/cli`](../internal/cli/cli.go) | The cobra command tree, flag parsing, exit codes, JSON output, diagnostics |
| [`fuzz`](../fuzz/fuzz.go) | Input generation, failure detection, delta-debugging minimisation, corpus |
| [`fuzz/vtgen`](../fuzz/vtgen/vtgen.go) | Generates the output side: terminal byte streams by grammar, for fuzzing a VT parser |
| [`fixtures`](../fixtures/fakeshell.go) | In-process helpers for testing an output consumer: a fake shell and an escape-sequence builder |
| [`tuiosx`](../tuiosx/tuiosx.go) | tuios-specific spawn and chord helpers; nothing in the core depends on it |

## Whole system

```mermaid
flowchart TB
  subgraph Bring["Bring your own"]
    PROG[program under test<br/>any binary, any language]
    SRC[tape file<br/>or a Go test function]
  end

  subgraph Front["Front ends"]
    CLIREG[internal/cli<br/>cobra command tree]
    GOTEST[go test<br/>StartT]
  end

  subgraph Lang["tape"]
    PARSE[parse<br/>tokens, positions, validation]
    PLAY[player<br/>one method per verb]
    REC[recorder<br/>timing policy]
    PRINT[print<br/>canonical formatter]
  end

  FUZZ[fuzz<br/>gen, detect, shrink]

  TERM[tuitest.Terminal<br/>waits, input, snapshots]

  subgraph Low["internal"]
    PTY[ptyproc.Process<br/>pty + exec + pump]
    EMU[emu.Emulator]
    VT[vt.Emulator<br/>vendored]
  end

  SRC --> PARSE --> PLAY --> TERM
  GOTEST --> TERM
  CLIREG --> PLAY
  CLIREG --> REC --> PRINT --> PARSE
  CLIREG --> FUZZ --> TERM
  FUZZ --> PLAY
  TERM --> PTY --> PROG
  PROG --> PTY --> TERM --> EMU --> VT
```

Two facts about this picture are worth stating in words.

First, the fuzzer does not have its own execution path. It generates
`tape.Command` values and replays candidates through the same player that
`tuitest run` uses. A minimised reproduction is therefore not a description of
what the fuzzer did, it is the identical execution, which is what makes the
generated tape trustworthy as a committed regression test.

Second, the recorder's output goes back through the parser in the round-trip
tests (`tape/roundtrip_test.go`, `tape/print_roundtrip_test.go`). Anything the
recorder can write, the parser can read, and printing a parsed tape reproduces
it. That closes the loop between `record` and `run` at the type level rather
than by convention.

## The read path

```mermaid
flowchart LR
  CHILD[child process] --> MASTER[PTY master]
  MASTER --> PUMP[pump goroutine<br/>32KB reads]
  PUMP --> LOCK[Terminal.onData<br/>takes t.mu]
  LOCK --> EMUW[emu.Write<br/>grid mutated]
  LOCK --> TAIL[tail ring<br/>last 4KB for errors]
  LOCK --> BCAST[cond.Broadcast]
  BCAST --> WAIT[waiting conditions re-evaluate]
  LOCK --> MIRROR[WithLog / WithOutputMirror]
  LOCK --> RESPQ[answers to queries<br/>queued]
  RESPQ --> RESP[responder goroutine<br/>writes them to the PTY]
  WAIT --> SNAP[viewLocked<br/>immutable Screen copy]
```

One goroutine reads the PTY master, and it is the only writer to the emulator.
Callbacks are never concurrent with each other, so `Terminal` only guards the
state that waits also read. Waiting conditions are evaluated while holding the
same lock the pump took, which is why a condition can never observe a torn grid,
and why `Screen` values handed to a `WaitFor` callback are safe to keep.

A snapshot copies the whole grid, so it is proportional to `cols * rows`: about
a tenth of a millisecond and 400KB at 120x40. It is cached until the grid
changes, so every reader of an unchanged screen shares one copy, and its text is
rendered once, on first use. Conditions that do not need a grid, such as
`WaitStable` and `WaitForOutput`, deliberately do not ask for one: during a heavy
output burst they would otherwise rebuild the screen on every 32KB chunk.

`viewLocked` is what every read goes through. It returns the live grid, except
while the program has a synchronized update (mode 2026) open: the emulator
reports the mode change from inside `Write`, before any byte of the new frame
reaches the grid, and the grid as it stands then is kept and shown until the
update closes, as a real terminal does. The hold ends after a second, or when
the child exits.

The pump never writes to the PTY. The emulator's answers to the program's
queries (cursor position, device attributes, colours) are queued, and a
separate goroutine writes them. Writing them from the pump used to deadlock a
program that asked faster than it read: its input buffer filled, the pump
blocked on the write and stopped reading output, and the program then blocked
writing its output. Caller input takes the queued answers with it, ahead of
itself, so the program still receives them in the order they were produced.

`WithLog` mirrors both directions and is what `StartT` wires to `t.Log`.
`WithOutputMirror` carries only what the program wrote, which is how `record`
and `replay` render the program onto a real terminal while the harness still
drives it headlessly.

## The write path

Input goes through one function, `Terminal.write`, which mirrors to the debug
log, timestamps `lastInput`, and hands the bytes to the PTY. The timestamp is
load-bearing: `WaitStable` measures its quiet window from the later of the last
output byte and the last input sent, so calling it straight after `SendKeys`
cannot report the pre-keystroke screen as stable. `Resize` takes the same path
for the same reason, since the redraw it provokes has not arrived yet.

## Process lifetime

```mermaid
flowchart LR
  START[ptyproc.Start] --> SETSID[setsid<br/>new session, PTY is the ctty]
  SETSID --> SLAVE[close the parent's slave fd]
  SLAVE --> PUMPG[pump goroutine]
  PUMPG --> EOF{read error?}
  EOF -- no --> PUMPG
  EOF -- yes --> REAP[reap once<br/>record code + signal]
  REAP --> ONCLOSE[OnClose handler]
  ONCLOSE --> DONE[close done channel]
  CLOSE[Process.Close] --> TREE[snapshot descendants]
  TREE --> GRP[signal the group and each descendant<br/>SIGTERM then SIGKILL]
  GRP --> PTYC[close the PTY]
```

Closing the parent's copy of the slave descriptor immediately after start is
what makes EOF happen at all: without it the parent keeps the slave open and the
master read blocks forever after the child exits.

The ordering at the end is deliberate. `OnClose` runs before `done` is closed,
so a caller woken by `Done()` cannot observe a `Terminal` that has not yet been
told the child is gone. `ExitCode` consults the process first and the terminal's
own copy second, closing the same window from the other side.

Teardown signals the process group rather than the process, and then every
descendant that left the group, such as a daemon that called `setsid`. That is
the property that makes tuitest usable against a multiplexer: a plain
`Process.Kill` would leave the daemon and every pane process running after the
test.

The group is not enough on its own, because a daemon calls `setsid` and leaves
it. So while the child is still running, `Close` first snapshots every
descendant by walking parent links, and signals each of them as well as the
group. The process table comes from `/proc` on Linux, from the `kern.proc.all`
sysctl on macOS, and from `ps` on the other Unixes. `Close` then waits for the
pump to reap the child, not merely for the child to die, so output the program
prints while shutting down still reaches the screen. It escalates to SIGKILL
after two seconds, and returns an error naming any process still alive after
that; `StartT` fails the test with it.

Once the child has exited and been reaped, parent links no longer lead anywhere:
its children were reparented to init. `Close` then signals the process group
only, and only if the child's pid has not been reused. A descendant that both
left the group and outlived the child cannot be found, and is not reported.

Input sent after the exit has been recorded is refused before it reaches the
PTY, with an error wrapping `ErrChildExited` that names the exit code. Input
that races the exit behaves differently by platform. Linux accepts writes to a
PTY whose program has gone until `Close` releases it. The bytes are discarded,
since `Start` closed the parent's copy of the other end and nothing is left to
read them, so the writes neither fail nor block. macOS fails them with EIO as
soon as the program closes its end, before the pump has reaped it.
`Terminal.write` and `Resize` wait up to a second for the reap in that case and
return an error wrapping `ErrChildExited`, so the caller finds `ExitStatus`
already reporting the exit when the error arrives.

## Errors

Failure types are structured rather than formatted strings, and each unwraps to
a sentinel so callers can branch without a type assertion.

| Type | Sentinel | Raised when |
| --- | --- | --- |
| `*tuitest.TimeoutError` | `ErrTimeout` | a wait ran out of time |
| `*tuitest.ClosedError` | `ErrChildExited` | the child exited before the condition held |
| (wrapped) | `ErrSemanticMarkers` | an OSC 133 wait without `WithSemanticMarkers` |
| `*tape.ParseError` | | a tape line would not parse |
| `*tape.LineError` | | any command failed, carrying its line number |
| `*tape.AssertionError` | | `ExpectExit`, or any other verb-level assertion |
| `*tape.ExpectError` | | an `Expect` regex did not match |
| `*tape.SnapshotError` | | a `Snapshot` did not match its golden |

The last three implement `tape.AssertionFailure`, an interface whose marker
method is unexported so only the `tape` package can claim the meaning. The CLI
maps every implementation to the assertion exit code, which means adding a new
assertion error type cannot silently start reporting a harness failure.
`SnapshotError` and `ExpectError` keep the two screens apart rather than
pre-rendering a diff, so `tuitest replay` can print them side by side while a
headless run still gets the unified diff from `Error()`.

`internal/cli.classify` maps these onto exit codes. The interesting judgement
there is `ErrChildExited`: a program that exits before a wait is satisfied, or
while the tape is still sending it input, is counted as an assertion failure
(exit 1), not a harness error (exit 3), because the harness did its job and the
program did not do what the tape said it would.
