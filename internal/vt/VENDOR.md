# Vendoring policy for internal/vt

This package is a copy, not a dependency. The non-test `.go` files here are
taken verbatim from tuios's `internal/vt`, so that tuitest interprets output
with the same emulator tuios renders through. The exact upstream revision is
recorded in [UPSTREAM](UPSTREAM), which is the single source of truth: every
sync updates it, and `TestVendoredCopyMatchesUpstream` checks the copy against
it when a tuios checkout is available.

## Why a copy

tuios's `internal/vt` is, as the name says, internal: Go will not let another
module import it. Promoting it to a public package in tuios would make tuitest
depend on the program it most often tests, and would freeze an emulator API
that is still moving. The copy is behind `internal/emu`, a nine-method
interface of which only five are needed for a working harness, so the emulator
can be replaced without touching tuitest's public surface. See
[docs/extending.md](../../docs/extending.md).

## The hazard

A copy silently rots. tuios fixes a wide-rune or scroll-region bug, tuitest
keeps the old behaviour, and a test that passes here fails against the real
terminal (or worse, the other way round). Nothing in the compiler notices.

## The rule

- The copy is downstream, never upstream. Fix emulator bugs in tuios first,
  then re-sync. A change made only here is guaranteed to be lost at the next
  sync, and the sync script will report it as drift.
- Only non-test `.go` files are copied. `doc.go` and the trimmed
  `emulator_test.go` here are tuitest's own and are left alone.
- Every sync updates `UPSTREAM` in the same commit as the copied files, so the
  provenance is never a guess.
- Sync deliberately, on a schedule or when chasing a fidelity bug, not
  automatically. An emulator change can move goldens, and that has to be
  reviewed rather than merged blind.

## Outstanding fixes to port upstream

The rule above says fixes go to tuios first. These went in here first, because
they were found by differential testing that lives in tuitest (see
`scripts/vtref/`), and they all exist in tuios's `internal/vt` at commit
59ce093 as well. **Port each of them to tuios and then re-sync**; until that
happens the next sync will report them as drift and reintroduce the bugs.

- `handlers.go`: SO and SI were registered inside the loop over C1 controls
  (0x80-0x9F), so the handlers for these two C0 controls (0x0E, 0x0F) were
  never installed and locking-shift line drawing printed raw ASCII.
- `utf8.go`: a wide rune printed with fewer than two columns left was written
  at the last column, where the buffer refused it and blanked the wide rune to
  its left, losing two characters off the end of every CJK or emoji line.
- `utf8.go`: insert mode (IRM) was tracked as a mode but never consulted when
  printing, so insertions overwrote instead of shifting the line right.
- `handlers.go`: ED 1 erased the whole cursor row instead of stopping at the
  cursor; ED 3 cleared the visible screen when it should only drop scrollback.
- `handlers.go`: DECSED and DECSEL (`CSI ? Ps J` / `CSI ? Ps K`) were
  unregistered and so silently did nothing.
- `handlers.go`: `CSI u` (SCORC) was missing although `CSI s` (SCOSC) saved.
- `handlers.go`, `cc.go`: `ESC E` (NEL), `ESC N` (SS2), `ESC O` (SS3) and
  `ESC # 8` (DECALN) had no handlers.
- `handlers.go`: `CSI j` (HPB) and `CSI k` (VPB) had no handlers.
- `csi_mode.go`: private mode 47, the original alternate screen and still
  smcup in older terminfo entries, was unhandled.
- `screen.go`: a cell shift for ICH or DCH ran through ultraviolet's
  `Buffer.Set`, which blanks the other half of any wide rune it lands on. The
  cells being moved are still live during a shift, so blanking the neighbour of
  a cell that had just been copied erased the copy, and the blanking cascaded:
  a single DCH on a line of CJK left the line empty. The shift now assigns and
  a single pass afterwards repairs any wide rune a shift cut in half. The
  underlying `Buffer.InsertCellArea` / `Buffer.DeleteCellArea` are still wrong
  and worth fixing in ultraviolet as well.
- `utf8.go`, `emulator.go`: the printable-ASCII fast path emitted its character
  immediately, so a combining mark arriving after it could not join it; the
  mark was written as a zero-width cell of its own, which lost the accent and
  blanked the next column. Clusters now fold into the cell in front of them
  when Unicode says the two are one grapheme. That also removes a source of
  chunk-dependent output: the grapheme buffer is flushed at the end of every
  `Write`, so where a PTY read fell used to decide whether a cluster formed.
- `csi_mode.go`: switching to the alternate screen homed the cursor. None of
  47, 1047 or 1049 is defined to move it.
- `csi_sgr.go`: `handleSgr` short-circuited to `uv.ReadStyle` whenever no theme
  colours were set, so the careful reader beside it only ever ran under a
  theme. Two bugs lived on the unthemed path as a result: an unrecognised
  underline subparameter such as `4:7` was left unconsumed and read on as a
  bare SGR 7, turning the cell reverse, and SGR 21 (double underline) was
  dropped. One reader now serves both cases.

Known divergences left alone deliberately are listed in
`scripts/vtref/README.md`.

## Syncing

```
scripts/vendor-vt.sh /path/to/tuios          # sync to that checkout's HEAD
scripts/vendor-vt.sh /path/to/tuios <commit> # sync to a specific commit
scripts/vendor-vt.sh -n /path/to/tuios       # report drift, change nothing
```

The script copies the upstream files, rewrites `UPSTREAM`, and prints what
changed. After it runs:

```
go test -race ./...
UPDATE_GOLDEN=1 go test ./...   # only if a golden legitimately moved
```

Review any golden diff as part of the sync commit. A golden that moves for a
reason nobody can explain is the signal that the sync introduced a regression,
which is the entire point of noticing it here rather than in a user's suite.
