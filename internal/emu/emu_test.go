package emu

import "testing"

// Modes promises only the modes that are set. The emulator's own table lists
// every mode it knows, reset ones as false, so a caller that ranges over the
// map, rather than looking up one mode, would take a reset mode for a set one.
func TestModesListsOnlySetModes(t *testing.T) {
	e := New(20, 4)
	if _, err := e.Write([]byte("\x1b[?1000h\x1b[?2004h\x1b[?2004l")); err != nil {
		t.Fatal(err)
	}
	modes := e.Modes()
	for mode, set := range modes {
		if !set {
			t.Errorf("mode %d is listed although it is reset", mode)
		}
	}
	if !modes[1000] {
		t.Error("mode 1000 was set but is not listed")
	}
	if _, ok := modes[2004]; ok {
		t.Error("mode 2004 was reset but is still listed")
	}
}
