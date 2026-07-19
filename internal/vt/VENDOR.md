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
that is still moving. The copy is behind `internal/emu`, a five-method
interface, so the emulator can be replaced without touching tuitest's public
surface.

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
