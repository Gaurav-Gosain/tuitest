//go:build !linux

package fuzz

// rssSampler is a no-op off Linux, where there is no cheap portable way to read
// another process's resident size. The memory check simply never fires; it does
// not report a false success, because a zero reading is treated as "no reading".
type rssSampler struct{}

func newRSSSampler(int) *rssSampler { return nil }

func (s *rssSampler) sample() uint64 { return 0 }
