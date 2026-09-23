//go:build !darwin && !windows

package ptyproc

// procTableNative has no implementation outside macOS. Linux reads /proc, and
// the other Unixes fall back to ps.
func procTableNative() map[int]procInfo { return nil }

// processZombie cannot answer without a native process table, so the caller
// falls back to the signal probe.
func processZombie(int) (zombie, known bool) { return false, false }
