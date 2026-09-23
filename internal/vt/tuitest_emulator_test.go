package vt_test

import (
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/vt"
)

// TestEmulator_WriteString verifies that WriteString writes through to the
// terminal without recursing into itself. Previously WriteString delegated
// to io.WriteString(e, s), which detects that *Emulator implements
// io.StringWriter and calls e.WriteString again, causing infinite recursion
// and a stack overflow.
func TestEmulator_WriteString(t *testing.T) {
	emu := vt.NewEmulator(80, 24)

	n, err := emu.WriteString("Hello, World!")
	if err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}
	if n != len("Hello, World!") {
		t.Errorf("Expected n=%d, got %d", len("Hello, World!"), n)
	}

	got := emu.String()
	if !strings.Contains(got, "Hello, World!") {
		t.Errorf("Expected output to contain %q, got %q", "Hello, World!", got)
	}
}
