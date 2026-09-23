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

## Teardown

**Teardown runs only if the test process lives to run it.** `StartT` closes the
terminal from `t.Cleanup`, which runs when a test fails, calls `t.Fatal`, or
panics in the test goroutine. It does not run when the test binary itself dies:
`go test -timeout` expiring, a panic in some other goroutine, or Ctrl+C. The
kernel then closes the PTY master and hangs up the terminal, which sends SIGHUP
to the program, and that is all. A program that ignores SIGHUP, and any daemon
it started with `setsid`, keeps running and has to be killed by hand. Give
`go test` a `-timeout` longer than the slowest test's own waits, so that a hang
fails a wait, and the cleanup runs, before the binary is killed.

**A descendant that left the process group is only found while the child
lives.** `Close` finds such a process by walking parent links from the child.
Once the child has exited and been reaped, its children belong to init and only
the process group is left to go on, so a daemon that called `setsid` and then
outlived the program that started it is neither killed nor reported.

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

Waits are close to free while idle. They block on a condition variable woken
by the pump and re-check every 5ms, and a screen that has not changed since
the last check is not copied again, so a suite's wall-clock time is the
program's own latency plus at most the 5ms poll interval per wall-clock
condition.

## Approximate, not exact

**`WaitStable` is a heuristic and cannot be made exact.** It measures its quiet
window from the later of the last output byte and the last input tuitest sent,
and does not settle before the child's first byte, so it can neither report the
pre-keystroke screen as stable nor return a blank screen from a program that is
slow to start. But a program that takes longer than the interval (150ms by
default) to react to input is still reported stable too early, and no
quiescence rule can distinguish that from a program with nothing to say. Wait
for the content you expect with `WaitForText`, `WaitForMatch`, `WaitFor` or
`WaitForStable` whenever you know it, and reach for `WaitStable` only after
heavy output with no specific end state.

**A write to a program that does not read its input blocks.** `SendKeys`,
`Type` and `Paste` write to the PTY, and once the program's input buffer is full
(about a kilobyte on macOS) the write waits for the program to read. A test that
pastes a large block into a program that has stopped reading hangs until the
`go test` timeout. The terminal's own answers to the program's queries do not
have this problem: they are queued and written separately, so the output side
keeps flowing.

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

**The PTY does not echo input or generate signals.** Before the child starts,
tuitest clears `ECHO`, `ISIG`, `IEXTEN` and `IXON` on the PTY. Line editing
(`ICANON`) and output processing are left as the kernel sets them. A TUI turns
all of these off itself, so for a TUI nothing changes except that input sent
before it finishes its terminal setup is no longer mangled. A line-oriented
program is different:

- What you type is not on the screen unless the program draws it. A shell
  script that runs `read name` shows the prompt and then its answer, with no
  `gopher` in between.
- `Ctrl('c')` sends a 0x03 byte and does not send SIGINT, so it does not
  interrupt `cat` or `sleep`. Signal the process yourself, or let `Close` tear
  it down.
- `^S` and `^Q` are ordinary bytes, not flow control.

The reasoning is in
[`internal/ptyproc/ptyproc_unix.go`](../internal/ptyproc/ptyproc_unix.go): with
the kernel defaults, whether a `Ctrl+c` in a tape reaches the program or kills
it depends on how fast the child was scheduled.

**`Screen.Line` returns one physical row.** A soft-wrapped logical line is not
de-wrapped, so text that wrapped across the right margin will not match as a
single string. Match per row, use `Screen.Text` and account for the wrap, or
widen the terminal with `WithSize` so the line fits.

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
inconsistency, memory growth, a replacement character and a violated invariant
are judged from outside the tape, by watching the process or by running a Go
closure, and any in-tape probe would mean sending input the fuzzer did not send,
so those files are transcripts and say so. Rerun `tuitest fuzz` against the
corpus to check a fix for them.

**The replacement-character check is only sound on well-formed input, and the
generator does not send it.** Hostile bursts carry bare continuation bytes,
overlong encodings and surrogate halves, and generated text is truncated
mid-rune one time in ten. Against those bytes drawing U+FFFD is correct, so the
check goes quiet for the whole run as soon as any of them is sent; there is no
bound on how long a program may hold input before drawing it, so a per-command
gate would not be sound. With the default generator it therefore almost never
fires. Pair it with `-no-hostile`, and accept that mid-rune truncation will
still silence some runs.

**An invariant is only judged at a settle, so a violation that repairs itself is
never seen.** The check runs after every command, but a finding is only produced
after a `WaitStable` or the end-of-iteration settle, because the fuzzer sends
input faster than a program consumes it and a screen caught mid-redraw fails a
reasonable invariant. A property that breaks at command ten and is back by
command forty produces nothing.

**An invariant needs guards, and writing them is the caller's problem.** The
fuzzer sends `Ctrl+c`, so a property about a running program must go vacuous
once it has exited. The fuzzer resizes to a single column and a single row,
where no program maintains a layout property, so a property about chrome must go
vacuous at a size where the chrome cannot fit. An under-guarded invariant
produces false positives; an over-guarded one goes vacuous on exactly the
degenerate sizes the fuzzer favours and is checked against almost nothing. There
is no way for tuitest to write these guards for you, because it does not know
what the program is supposed to draw.

**`memory-growth` is Linux only.** It reads `/proc/<pid>/statm`; on other
platforms the sampler reports nothing and the check never fires, which is why it
is off by default rather than silently platform-dependent.

**A minimised reproduction may not re-verify.** Every finding is replayed once
after minimisation, and one that does not reproduce is still reported but
labelled in both the report and the tape header. A timing-dependent bug is worth
knowing about; presenting it as solid would not be.

**The fuzzer's own tests can inherit that flakiness.**
`TestFindsPanicAndMinimisesToTheTriggeringKey` and
`TestFindsTerminalLeftInABadState` in
`fuzz/fuzz_test.go` assert `Failure.Verified`, which means they assert that a
minimised reproduction re-reproduced on the confirmation replay. That is exactly
the property the paragraph above says is not guaranteed, and both used to fail
intermittently under load.

Part of that was a harness bug, since fixed: a write that raced the program's
exit was classified before the child had been reaped, so a program that had
quit cleanly was reported as `crash: driving the program failed: write
/dev/ptmx: input/output error`. A dirty-exit reproduction confirmed as a crash
does not match, and is labelled unverified. On an 11-core macOS machine, twelve
parallel copies of the two tests, four runs each, failed three times before the
fix and not at all after it. Whether any flakiness remains on Linux has not been
measured since. If it does, nothing is wrong with the fuzzer when it fires; the
assertion is stricter than the behaviour it tests, and a retry, or demoting the
`Verified` check to a report rather than a failure, would fix it.

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
