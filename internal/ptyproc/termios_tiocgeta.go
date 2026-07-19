//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package ptyproc

import "golang.org/x/sys/unix"

// See termios_tcgets.go for what these are and why the non-flushing setter is
// the right one.
const (
	getTermios = unix.TIOCGETA
	setTermios = unix.TIOCSETA
)
