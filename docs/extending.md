# Extending tuitest

Every part of tuitest that could reasonably need replacing sits behind a small
interface or a single registration point. This page is one section per seam:
what the contract is, where the code lives, and what it costs to take it over.

## Swap the VT emulator

The emulator is reached through one interface in
[`internal/emu`](../internal/emu/emu.go):

```go
type Emulator interface {
	Write(p []byte) (int, error)
	Resize(cols, rows int)
	Size() (cols, rows int)
	CellAt(col, row int) *uv.Cell
	Cursor() (col, row int, visible bool)
	PromptCount() int
	CommandFinishedCount() int
	LastCommandExit() (code int, ok bool)
	Modes() map[int]bool
}
```

Nine methods, and only the first five are needed for a working harness: the OSC
133 trio can return zero values if the emulator does not track semantic markers
(the corresponding waits then never fire, which is the same behaviour as a
program that emits no markers), and `Modes` can return an empty map if it does
not track private modes, at the cost of `TermState.Dirty()` always reporting
clean.

The contract is that `Write` is called only from the pump goroutine while the
`Terminal` lock is held, so an implementation needs no internal locking for the
write path; `CellAt` may return `nil` for out-of-bounds coordinates and the
adapter converts that to a blank cell; `Modes` returns only the modes currently
set, keyed by DEC private mode number.

Everything above this interface is written against it, and `emu.New` is the only
place the concrete type is named:

```go
func New(cols, rows int) Emulator {
	return &adapter{e: vt.NewEmulator(cols, rows)}
}
```

Because the package is internal, the emulator choice is not part of tuitest's
public contract, so replacing it is not a breaking change for any user.

## Re-sync or replace the vendored VT

[`internal/vt`](../internal/vt) is a copy of tuios's interpreter, not a
dependency, for the reasons set out in
[VENDOR.md](../internal/vt/VENDOR.md): the upstream package is internal, and
promoting it would make tuitest depend on the program it most often tests while
freezing an API that is still moving.

```
scripts/vendor-vt.sh /path/to/tuios          # sync to that checkout's HEAD
scripts/vendor-vt.sh /path/to/tuios <commit> # sync to a specific commit
scripts/vendor-vt.sh -n /path/to/tuios       # report drift, change nothing
```

The rule is that the copy is downstream, never upstream. Fix emulator bugs in
tuios first, then re-sync; a change made only here is guaranteed to be lost at
the next sync, and the script reports it as drift. `TestVendoredCopyMatchesUpstream`
checks the copy against `internal/vt/UPSTREAM` when `TUITEST_TUIOS_SRC` points at
a checkout. After a sync, run the suite and review any golden that moved as part
of the sync commit: a golden that moves for a reason nobody can explain is the
signal that the sync introduced a regression.

## Add a CLI subcommand

The command set is a cobra tree assembled by `newRootCommand` in
[`internal/cli/cli.go`](../internal/cli/cli.go). A command is a constructor that
takes the `Env` and returns a `*cobra.Command`:

```go
func snapCommand(env *Env) *cobra.Command { ... }
```

Add it to the `root.AddCommand` call and help, shell completion, and the
nearest-match suggestion for a typo all pick it up, because each of those reads
the same tree. `Env` carries the writers and the environment lookup, so a
command is testable without a real terminal; the whole CLI test suite runs
against synthetic writers, and the root command is pointed at those same writers
so cobra's own output is captured too.

Write the help the way the rest of the tree does: a `Short` that fits one line, a
`Long` that says when you would reach for the command rather than restating its
flags, and an `Example` block with a comment above each invocation.

Failures do not return a bare exit code. Return `fail(err)` to let `classify`
pick between `ExitAssert`, `ExitHarness` and `ExitTimeout` from the error type,
`failWith(code, err)` when the code is not derived from an error, `silent(code)`
when the command has already printed its own report, and `usageErrorf` for a
malformed invocation. Anything else returned from `RunE` is classified as if it
had come from `fail`.

## Add a tape verb

A verb touches five places, all mechanical
([`tape/parse.go`](../tape/parse.go), [`tape/player.go`](../tape/player.go),
[`tape/print.go`](../tape/print.go)):

1. A `Kind` constant.
2. A case in `Kind.Verb()` giving its canonical spelling.
3. A case in the parser that validates arguments and fills `Command`.
4. A case in the player that executes it against a `*tuitest.Terminal`.
5. A case in the printer so `Sprint` can render it.

The round-trip tests then cover it for free: anything the printer emits, the
parser must read back to the same `Command`. If the verb is an assertion, its
error type must implement `tape.AssertionFailure` so the CLI maps it to exit
code 1; the marker method is unexported, which is what stops a new error type
from silently reporting as a harness failure.

## Drive the harness from your own runner

Nothing about the tape language is privileged. `tape` and `fuzz` are both
ordinary callers of the public `*tuitest.Terminal`, and `Diff` is exported
specifically so an out-of-package golden runner can produce the same encoding
`AssertGolden` does. If you want a different scripting language, a different
report format, or an integration with another test framework, write it against
the root package and leave `tape` alone.

`fuzz.Run(ctx, fuzz.Options{...})` is likewise usable directly, and returns a
`*fuzz.Result` with one `*fuzz.Failure` per distinct kind found, so a project
can run the fuzzer from its own `go test` rather than from the CLI.

## Add project-specific helpers

[`tuiosx`](../tuiosx/tuiosx.go) is the worked example: 69 lines of tuios-specific
conveniences (a leader-chord helper, a binary locator, a spawn helper that gives
each instance its own temporary XDG directories) living in their own package.
Nothing in the core depends on it and it can be deleted without touching the
harness. Do the same for your project rather than adding options to `Start`:
an option that only one program needs is a cost every other user pays for in
surface area.

## Replace a whole layer

If none of the seams above fits, the layer you want is probably smaller than you
expect. `internal/ptyproc` is roughly 200 lines of process and PTY lifetime with
no knowledge of screens; the root package's `Terminal` is roughly 400 lines of
waits, input and snapshots with no knowledge of `exec`. Either can be
reimplemented against the other's contract, and the split between them is the
place to cut. Forking one of those two files is a more honest outcome than
bending the harness with options, and the tests in `internal/ptyproc` and the
root package describe the contract each side has to keep.
