//go:build linux || solaris || aix

package ptyproc

import "golang.org/x/sys/unix"

// The ioctls that read and write terminal settings. Linux and the System V
// line spell them TCGETS and TCSETS; the BSDs, macOS included, spell them
// TIOCGETA and TIOCSETA, in termios_tiocgeta.go.
//
// The setting ioctl is the non-flushing one deliberately. Its flushing variant
// would discard input already queued on the PTY, and tuitest may have written
// keystrokes before the program got round to reading them.
const (
	getTermios = unix.TCGETS
	setTermios = unix.TCSETS
)
