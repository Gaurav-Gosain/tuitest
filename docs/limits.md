# Limits

What tuitest cannot do, what it does approximately, and where the right answer
is a different tool. This page is the one that gets updated when something is
found to be untrue.

## Platform

**Unix only, and Windows deliberately fails to build.**

The PTY layer underneath tuitest would work through ConPTY, but neither
guarantee tuitest makes on top of it would: a child cannot be put in its own
session with the PTY as its controlling terminal, and there is no process group
to signal at teardown, so a multiplexer's daemon and its pane processes would
survive every test. Rather than ship stubs that compile and then leak processes
at run time, [`internal/ptyproc/ptyproc_windows.go`](../internal/ptyproc/ptyproc_windows.go)
references an undefined identifier whose name is the error message.

Use WSL, or a Unix runner. Adding a real backend means a ConPTY spawn plus a Job
Object per child so that teardown is transitive; that file is where the work
starts. `tuitest doctor` reports the platform as a warning rather than a failure,
since a Windows binary cannot exist to run it.

Everything else needs a `/dev/ptmx` that can be opened. A container with a
restricted `/dev` and no `/dev/pts` fails the doctor's PTY check with that hint.

## What lives in memory

| Thing | Size | Notes |
| --- | --- | --- |
| Emulator grid | one `uv.Cell` per cell | 80x24 is 1,920 cells; 1000x1000 is a million |
| Snapshot | one `Cell` per cell, copied | built per condition evaluation that needs one |
| I/O tail ring | 4KB | fixed, for error messages |
| Pump buffer | 32KB | fixed, reused |
| Scrollback | as the vendored VT allocates | not exposed through `Screen` |

The rule of thumb: memory is `cols * rows` per live terminal plus a snapshot for
each wait currently evaluating a grid condition, and everything else is
constant. A tape's `Set Size` and `Resize` are bounded to 1..10000 per dimension
precisely because the grid is allocated up front and a tape is untrusted input
to the CLI.

Nothing is on disk except golden files and the fuzz corpus, and nothing is
shared between terminals, so parallel tests scale with grid size rather than
contending.

## Throughput

Measured on an Intel i7-10700 (16 threads, Linux), 80-column grid, five runs of
`go test -run '^$' -bench . -benchtime 3s .`, from `bench_test.go`. Ranges
rather than single figures, because this was an otherwise-busy desktop and the
spread is real:

| Workload | Lines per second | Bytes per second |
| --- | --- | --- |
| Plain 80-column text lines | 64,000 to 68,000 | 5.2 to 5.5 MB/s |
| Same with an SGR change per line | 44,000 to 66,000 | 4.3 to 6.4 MB/s |

Adding more concurrency cannot make this faster. A VT interpreter is a state
machine over an ordered byte stream: cell N+1 depends on every escape sequence
before it, so the work cannot be split. A program that emits far more than this
feels PTY backpressure rather than losing data, so a heavy-output test needs a
timeout sized for the volume, not for the harness.

Waits themselves are free while idle. They block on a condition variable woken
by the pump, so a suite's wall-clock time is the program's own latency plus at
most the 5ms poll interval per wall-clock condition.

## Approximate, not exact

**`WaitStable` is a heuristic and cannot be made exact.** It measures its quiet
window from the later of the last output byte and the last input tuitest sent,
and its first window from spawn, so it can neither report the pre-keystroke
screen as stable nor report stability before the child has produced anything.
But a program that takes longer than the interval (150ms by default) to produce
its first byte is still reported stable too early, and no quiescence rule can
distinguish that from a program with nothing to say. Wait for the content you
expect with `WaitForText`, `WaitForMatch` or `WaitFor` whenever you know it, and
reach for `WaitStable` only after heavy output with no specific end state.

**Hang detection is the fuzzer's one heuristic.** There is no universal liveness
probe for a TUI: no key is guaranteed to produce output, and the program does
not answer the status queries a terminal would. The check requires several
unanswered inputs and then a full grace period, which is tuned to stay quiet, so
it will miss a program that wedged while a draw was already in flight.

**A recorded wait is only as distinctive as the screen was.** If nothing
identifiable appeared, `tuitest record` falls back to `WaitStable`, which is
weaker. Skim a recording before trusting it as a test; that is why the output is
written to be read and edited.

## Fidelity gaps

**`Screen.Line` returns one physical row.** A soft-wrapped logical line is not
de-wrapped, so text that wrapped across the right margin will not match as a
single string. Match per row, use `Screen.Text` and account for the wrap, or
widen the terminal with `WithSize` so the line fits.

**`Cell.Rune` is the cell's first rune only.** Combining marks are not exposed,
so a cell holding `e` plus a combining acute is indistinguishable from a plain
`e` at the assertion level. The underlying emulator keeps the full content; the
public `Cell` does not surface it.

**Scrollback is not reachable through `Screen`.** The vendored VT maintains it
and `tuitest doctor` reports it as supported, but the public interface exposes
only the visible grid. A test that needs to assert on scrolled-off content has
to capture it as it passes.

**`SendMouse` emits SGR (mode 1006).** A `Mouse` line recorded from an X10 or
urxvt report carries the encoding it was recorded with and replays in that
encoding, but a `Mouse` line written by hand is sent as SGR, so a program that
enables only the older encodings will not see it.

**Mouse mode 1005 is decoded as X10.** The UTF-8 mouse encoding is
indistinguishable from X10 without knowing the mode, so its reports are read as
X10 and the coordinates written on the `Mouse` line are wrong above column 95.
The bytes are still captured and replayed exactly, so the recording is faithful
even where the line is not readable.

**A recorded `Enter` may appear as `Ctrl+j`.** In raw mode a terminal sends 0x0d
for Enter and the recorder names that `Enter`. Some pipes deliver 0x0a instead,
which is `Ctrl+j`; naming it `Enter` would replay different bytes than were
recorded, so the literal name is kept.

## The vendored emulator

`internal/vt` is a copy of tuios's interpreter rather than an external
dependency, so it does not pick up upstream fixes automatically. This is the
gap most likely to produce a wrong answer without producing an error: tuios
fixes a wide-rune or scroll-region bug, tuitest keeps the old behaviour, and a
test that passes here fails against the real terminal, or worse, the other way
round. Nothing in the compiler notices.

The mitigations are provenance and a drift check, not automation. The exact
upstream commit is recorded in [`internal/vt/UPSTREAM`](../internal/vt/UPSTREAM),
the policy is [VENDOR.md](../internal/vt/VENDOR.md), `scripts/vendor-vt.sh -n
/path/to/tuios` reports drift without changing anything, and
`TUITEST_TUIOS_SRC=/path/to/tuios go test ./internal/vt/...` makes the suite
check the copy against the recorded commit. Syncing is deliberate, on a schedule
or when chasing a fidelity bug, because an emulator change can move goldens and
that has to be reviewed rather than merged blind.

## Fuzzing

**Generation is blind.** There is no coverage instrumentation of the program
under test, so input is generated from a structural model rather than steered
toward new code paths. It finds shallow bugs quickly and deep ones only by luck,
and raising `-iterations` has diminishing returns much sooner than a
coverage-guided fuzzer would. If you control the source and it is in Go,
`go test -fuzz` against the update loop will reach places this cannot.

**Only crash and dirty-exit reproductions carry an assertion.** Those end in
`ExpectExit 0`, so the file is red until the bug is fixed. A hang, a screen
inconsistency and memory growth are judged from outside the tape by watching the
process, and any in-tape liveness probe would mean sending input the fuzzer did
not send, so those files are transcripts and say so. Rerun `tuitest fuzz`
against the corpus to check a fix for them.

**`memory-growth` is Linux only.** It reads `/proc/<pid>/statm`; on other
platforms the sampler reports nothing and the check never fires, which is why it
is off by default rather than silently platform-dependent.

**A minimised reproduction may not re-verify.** Every finding is replayed once
after minimisation, and one that does not reproduce is still reported but
labelled in both the report and the tape header. A timing-dependent bug is worth
knowing about; presenting it as solid would not be.

**The fuzzer's own tests inherit that flakiness, and two of them are flaky
today.** `TestFindsPanicAndMinimisesToTheTriggeringKey` and
`TestFindsTerminalLeftInABadState` in
`fuzz/fuzz_test.go` assert `Failure.Verified`, which means they assert that a
minimised reproduction re-reproduced on the confirmation replay. That is exactly
the property the paragraph above says is not guaranteed. Both pass in isolation
and fail intermittently under load, because confirmation drives a real program
through a real PTY and a loaded machine can miss the timing window: on a
16-thread desktop, running the package six ways in parallel reproduces
`the minimised reproduction did not reproduce on confirmation` within a handful
of attempts, and a plain `go test ./...` hits it occasionally. Nothing is wrong
with the fuzzer when this fires; the assertion is stricter than the behaviour it
tests. A retry, or demoting the `Verified` check to a report rather than a
failure, would fix it.

## What this is not for

tuitest is not a benchmark harness. It measures nothing about the program under
test except byte counts and wall-clock latency between input and output, and the
emulator sits between you and any timing you might want to measure. For frame
timing or render cost, instrument the program.

It is not a visual regression tool. Goldens are text and attribute runs, not
images, so a change that alters colour or layout shows as a text diff and a
change that alters nothing textual is invisible. That is a deliberate trade: a
text golden is reviewable in a pull request and an image is not.

It is not an in-process test framework. Every assertion costs a spawn and a
round trip through a PTY, so a suite of hundreds of tape files is seconds, not
milliseconds. For fast unit tests of a Bubble Tea update loop, `teatest` runs in
process and is the right tool; use tuitest for what only a real terminal can
tell you.
