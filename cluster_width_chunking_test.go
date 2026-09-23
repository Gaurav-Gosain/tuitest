package tuitest

import (
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// TestChunkingKeepsClusterWidths writes streams whose clusters change width
// when their last rune arrives, split at every byte, and asserts each split
// draws the same cells and leaves the cursor in the same place as the unsplit
// write.
//
// FuzzChunkingIsInvisible compares Text, which cannot see a width: a heart
// drawn one column wide and a heart drawn two read the same. That is how this
// got through. A variation selector split from its base by a PTY read left the
// base one column wide and put every following character one column early, so
// tuios's wide rune differential in e2e/tui failed or passed depending on where
// the kernel ended a read.
func TestChunkingKeepsClusterWidths(t *testing.T) {
	const cols, rows = 12, 3
	for _, tc := range []struct {
		name string
		in   string
		// The unsplit write must draw a two column cluster here, or a split
		// that gets the width wrong in the same way would pass.
		wideX, wideY int
	}{
		{"presentation selector", "ab❤️❤️❤️cd", 2, 0},
		{"keycap", "1️⃣x", 0, 0},
		{"selector at the right margin", "0123456789a❤️z", 0, 1},
		{"selector at the last two columns", "0123456789❤️z", 10, 0},
		{"selector then styled text", "❤️\x1b[1mb\x1b[0m", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.in)
			whole := emu.New(cols, rows)
			if _, err := whole.Write(data); err != nil {
				t.Fatalf("write: %v", err)
			}
			want := (&Terminal{emu: whole, exitCode: -1}).snapshotLocked()
			wantX, wantY, _ := whole.Cursor()
			if c := want.Cell(tc.wideX, tc.wideY); c.Width != 2 {
				t.Fatalf("unsplit write: cell (%d,%d) is %q width %d, want a two column cluster",
					tc.wideX, tc.wideY, c.Content, c.Width)
			}

			for at := 1; at < len(data); at++ {
				split := emu.New(cols, rows)
				if _, err := split.Write(data[:at]); err != nil {
					t.Fatalf("write: %v", err)
				}
				if _, err := split.Write(data[at:]); err != nil {
					t.Fatalf("write: %v", err)
				}
				got := (&Terminal{emu: split, exitCode: -1}).snapshotLocked()
				for r := range rows {
					for c := range cols {
						w, g := want.Cell(c, r), got.Cell(c, r)
						if w.Content != g.Content || w.Width != g.Width {
							t.Fatalf("split at byte %d: cell (%d,%d) is %q width %d, want %q width %d",
								at, c, r, g.Content, g.Width, w.Content, w.Width)
						}
					}
				}
				if x, y, _ := split.Cursor(); x != wantX || y != wantY {
					t.Fatalf("split at byte %d: cursor at (%d,%d), want (%d,%d)", at, x, y, wantX, wantY)
				}
			}
		})
	}
}
