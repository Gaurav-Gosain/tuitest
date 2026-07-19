package tuitest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/internal/emu"
)

// Differential testing of the vendored emulator against ghostty-vt.
//
// The harness's whole value is that it reports what a program actually
// rendered, so "is our emulator right?" cannot be answered by our own unit
// tests alone: they encode the same assumptions the emulator does. This test
// feeds identical bytes to internal/vt and to Ghostty's VT (compiled to wasm
// and driven by node) and compares the resulting screens cell by cell, plus the
// cursor and the alternate-screen flag.
//
// It is opt-in because it needs node and a wasm blob that is not vendored here:
//
//	TUITEST_VT_REF=/path/to/ref go test -run TestVTDifferential ./...
//
// where /path/to/ref holds ghostty-vt.wasm and the dump.mjs from
// scripts/vtref/. Divergences it finds are not left to this test to catch:
// each one is copied down into an ordinary unit test with ghostty's answer
// hard-coded, so the fix stays enforced on a machine with no node at all.

// refCell is one cell in the interchange format both dumpers emit: the rune,
// the display width, and the attribute string in the same encoding
// SnapshotStyled uses, so a mismatch reads the way a golden diff does.
type refCell struct {
	R string `json:"r"`
	W int    `json:"w"`
	A string `json:"a"`
}

type refDump struct {
	Cols   int `json:"cols"`
	Rows   int `json:"rows"`
	Cursor struct {
		X       int  `json:"x"`
		Y       int  `json:"y"`
		Visible bool `json:"visible"`
	} `json:"cursor"`
	Alt  bool        `json:"alt"`
	Grid [][]refCell `json:"grid"`
}

// dumpOurs runs the vendored emulator over the input and returns the dump in
// the interchange format.
func dumpOurs(input []byte, cols, rows int) refDump {
	e := emu.New(cols, rows)
	// Feed in chunks so the parser has to resume across boundaries the same
	// way it does on a real PTY read.
	const chunk = 4096
	for off := 0; off < len(input); off += chunk {
		end := min(off+chunk, len(input))
		_, _ = e.Write(input[off:end])
	}

	var d refDump
	d.Cols, d.Rows = cols, rows
	d.Grid = make([][]refCell, rows)
	for row := range rows {
		line := make([]refCell, cols)
		for col := range cols {
			c := toCell(e.CellAt(col, row))
			line[col] = refCell{R: string(c.Rune), W: c.Width, A: cellAttrs(c)}
		}
		d.Grid[row] = line
	}
	d.Cursor.X, d.Cursor.Y, d.Cursor.Visible = e.Cursor()
	modes := e.Modes()
	d.Alt = modes[modeAltScreenBare] || modes[modeAltScreen] || modes[modeAltScreenSaveCursor]
	return d
}

func dumpRef(t *testing.T, refDir, file string, cols, rows int) refDump {
	t.Helper()
	cmd := exec.Command("node", filepath.Join(refDir, "dump.mjs"),
		fmt.Sprint(cols), fmt.Sprint(rows), file)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("reference dump of %s: %v", file, err)
	}
	var d refDump
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatalf("decode reference dump of %s: %v", file, err)
	}
	return d
}

func TestVTDifferential(t *testing.T) {
	refDir := os.Getenv("TUITEST_VT_REF")
	if refDir == "" {
		t.Skip("set TUITEST_VT_REF to a directory holding ghostty-vt.wasm and dump.mjs")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}

	files, err := filepath.Glob(filepath.Join("testdata", "vtcorpus", "*.vt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus in testdata/vtcorpus: %v", err)
	}

	const cols, rows = 80, 24
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".vt"), func(t *testing.T) {
			input, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			want := dumpRef(t, refDir, file, cols, rows)
			got := dumpOurs(input, cols, rows)

			if got.Cursor != want.Cursor {
				t.Errorf("cursor: got %+v, want %+v", got.Cursor, want.Cursor)
			}
			if got.Alt != want.Alt {
				t.Errorf("alt screen: got %v, want %v", got.Alt, want.Alt)
			}
			// Report at most a handful of cell divergences per file; a
			// systematic bug otherwise buries the next one under thousands of
			// identical lines.
			const maxReport = 8
			n := 0
			for row := range rows {
				for col := range cols {
					g, w := got.Grid[row][col], want.Grid[row][col]
					if g == w {
						continue
					}
					n++
					if n <= maxReport {
						t.Errorf("cell (%d,%d): got %+v, want %+v", col, row, g, w)
					}
				}
			}
			if n > maxReport {
				t.Errorf("...and %d further cell divergences", n-maxReport)
			}
		})
	}
}
