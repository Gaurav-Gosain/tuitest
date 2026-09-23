# Vendoring policy for internal/vt

This package is a copy, not a dependency. The files here are taken from tuios's
`internal/vt`, so that tuitest interprets output with the same emulator tuios
renders through. The exact upstream revision is recorded in
[UPSTREAM](UPSTREAM), which is the single source of truth: every sync updates
it, and `TestVendoredCopyMatchesUpstream` checks the copy against it when a
tuios checkout is available.

## Why a copy

tuios's `internal/vt` is, as the name says, internal: Go will not let another
module import it. Promoting it to a public package in tuios would make tuitest
depend on the program it most often tests, and would freeze an emulator API
that is still moving. The copy is behind `internal/emu`, a narrow interface, so
the emulator can be replaced without touching tuitest's public surface. See
[docs/extending.md](../../docs/extending.md).

## The hazard

A copy silently rots. tuios fixes a wide-rune or scroll-region bug, tuitest
keeps the old behaviour, and a test that passes here fails against the real
terminal (or worse, the other way round). Nothing in the compiler notices.

## What is copied

- Every non-test `.go` file, except the libghostty backend (the files whose
  build constraint is exactly `ghostty`). That backend needs cgo and a pinned
  native library, and tuitest only builds the pure Go emulator.
- Every test file that does not need tuios's `internal/testutil`, with the
  import paths of `internal/vt` and `internal/fuzz/vtgen` rewritten to
  tuitest's. This carries the conformance corpus, the unicode sweeps and the
  fuzz targets across, so a sync brings the tests that pin what it brings.
  tuios's `emulator_test.go` and `main_test.go` need `testutil` and stay behind.
- Everything under `testdata/`. Files that only exist here, such as fuzz corpus
  entries found by tuitest, are left alone.

Files that belong to tuitest are never touched by a sync: `doc.go`, this file,
`UPSTREAM`, `DIVERGENCE`, and every file named `tuitest_*.go`. That includes
`tuitest_responses.go`, which adds the non-blocking `TakeResponses` that
`internal/emu` drains after each write; tuios reads responses with a goroutine
blocked in `Read` instead, so it has no need for it.

## The rule

- The copy is downstream. Fix emulator bugs in tuios first, then re-sync.
- When a fix has to land here first, list the file in [DIVERGENCE](DIVERGENCE)
  and describe the change below. A sync then merges upstream changes into the
  file with `git merge-file` instead of overwriting it, and stops on a conflict.
  The drift check fails if a listed file matches upstream (the fix landed, drop
  the line) or an unlisted one does not (an unrecorded local edit).
- Every sync updates `UPSTREAM` in the same commit as the copied files, so the
  provenance is never a guess.
- Sync deliberately, on a schedule or when chasing a fidelity bug, not
  automatically. An emulator change can move goldens, and that has to be
  reviewed rather than merged blind.

## Outstanding fixes to port upstream

tuios took most of what this copy used to carry: SO and SI, the right margin
wide rune, IRM, ED 1 and ED 3, SCORC, NEL, SS2, SS3, DECALN, the ICH and DCH
wide rune shift, clusters folded across a write boundary, the CPR line and
column order, the locked mode map read, the clamped ECH count and scroll
region, and the clamped cursor and severed wide rune after a resize. These are
still only here. **Port each of them to tuios and then re-sync.**

- `csi_sgr.go`: `handleSgr` hands every SGR without a theme to `uv.ReadStyle`,
  which reads an unknown underline style such as `4:7` on as a bare SGR 7,
  turning the cell reverse, and drops SGR 21 (double underline). Here every SGR
  goes through `readStyleWithTheme`, which consumes the subparameter and knows
  21; the lone truecolor shortcut is kept. `csi_sgr_rgb_test.go` compares the
  shortcut with `readStyleWithTheme` instead of `uv.ReadStyle` to match. tuios's
  own `TestThemedSGR_UnderlineSubparamNoLeak` shows the themed path already
  guards the `4:7` case, so this only makes the unthemed path agree with it.
- `csi_mode.go`, `mode.go`: private mode 47, the original alternate screen and
  still smcup in older terminfo entries, is unhandled upstream, so a program
  using it draws over the primary screen and never gets it back. Here 47 is
  handled with 1047. Leaving either restores the cursor the alternate screen
  had, and a reset sent while the primary screen is already up does nothing.
- `csi_mode.go`: switching to the alternate screen homes the cursor upstream.
  None of 47, 1047 or 1049 is defined to move it, and xterm, tmux and ghostty
  leave it where it stood. `conform_screen_test.go` carries the matching
  expectation.
- `mode.go`: DECRQM answers out of the mode table, and upstream's table omits
  2048 (in-band resize), which the emulator acts on. It is answered "not
  recognized", so a program that probes before enabling takes its fallback
  path.
- `handlers.go`: DECSED and DECSEL (`CSI ? Ps J` and `CSI ? Ps K`) are
  unregistered upstream, so they erase nothing. Nothing tracks DECSCA, so every
  cell is unprotected and a selective erase is a plain one. tuios's corpus
  records the gap on purpose; `conform_erase_test.go` carries the expectation
  this copy meets instead.
- `handlers.go`: `CSI j` (HPB) and `CSI k` (VPB) have no handlers upstream.

Known divergences left alone deliberately are listed in
`scripts/vtref/README.md`.

## Syncing

```
scripts/vendor-vt.sh /path/to/tuios          # sync to that checkout's HEAD
scripts/vendor-vt.sh /path/to/tuios <commit> # sync to a specific commit
scripts/vendor-vt.sh -n /path/to/tuios       # report drift, change nothing
```

The script copies the upstream files, merges the listed ones, rewrites
`UPSTREAM`, and prints what changed. It names any file here that upstream no
longer has; remove those by hand. Then run:

```
gofmt -l .
go mod tidy                     # when upstream moved ultraviolet or x/ansi
go test -race ./...
TUITEST_TUIOS_SRC=/path/to/tuios go test ./internal/vt/
UPDATE_GOLDEN=1 go test ./...   # only if a golden legitimately moved
```

Pin `github.com/charmbracelet/ultraviolet` and `github.com/charmbracelet/x/ansi`
in `go.mod` to the versions tuios's `go.mod` has at the synced commit. The
emulator is written against those versions, and a different ultraviolet can
change what a cell reads back as without any file here changing.

Review any golden diff as part of the sync commit. A golden that moves for a
reason nobody can explain is the signal that the sync introduced a regression,
which is the entire point of noticing it here rather than in a user's suite.
