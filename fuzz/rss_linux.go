//go:build linux

package fuzz

import (
	"os"
	"strconv"
	"strings"
)

// rssSampler reads resident set size for a process from /proc. Memory growth in
// the program under test is only observable from outside, so this is as close
// as the harness can get to a leak detector without cooperation from the
// program. It reads the whole process group leader only; a program that leaks
// in a child is not covered.
type rssSampler struct {
	path     string
	pageSize uint64
}

func newRSSSampler(pid int) *rssSampler {
	if pid <= 0 {
		return nil
	}
	return &rssSampler{
		path:     "/proc/" + strconv.Itoa(pid) + "/statm",
		pageSize: uint64(os.Getpagesize()),
	}
}

// sample returns resident bytes, or 0 when the process is gone or unreadable.
// Callers treat 0 as "no reading", never as "no memory".
func (s *rssSampler) sample() uint64 {
	if s == nil {
		return 0
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return 0
	}
	// statm fields: size resident shared text lib data dt, in pages.
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * s.pageSize
}
