//go:build windows

package main

import "github.com/Gaurav-Gosain/tuitest/tape"

// watchResize is a no-op on Windows, which has no SIGWINCH. Recording still
// works; only mid-session resizes go uncaptured. This mirrors the platform stub
// in internal/ptyproc.
func watchResize(uintptr) (<-chan tape.Size, func()) {
	return nil, func() {}
}
