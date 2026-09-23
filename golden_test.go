package tuitest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// recordingTB captures the failures assertGolden reports.
type recordingTB struct {
	testing.TB
	failures []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// TestGoldenToleratesEditorLineEndings covers golden files as they come back
// from an editor or a Windows checkout: a trailing newline, or CRLF endings.
// A snapshot has neither, so every such golden used to fail with a diff that
// showed no visible difference.
func TestGoldenToleratesEditorLineEndings(t *testing.T) {
	t.Setenv("UPDATE_GOLDEN", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	const got = "line one\nline two"
	cases := map[string]string{
		"exact":            got,
		"trailing newline": got + "\n",
		"crlf":             "line one\r\nline two\r\n",
	}
	for name, content := range cases {
		if err := os.WriteFile(filepath.Join("testdata", "g.golden"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		tb := &recordingTB{}
		assertGolden(tb, "g", got)
		if len(tb.failures) != 0 {
			t.Errorf("%s: golden %q did not match %q: %v", name, content, got, tb.failures)
		}
	}

	// A real difference still fails.
	if err := os.WriteFile(filepath.Join("testdata", "g.golden"), []byte("line one\nline 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tb := &recordingTB{}
	assertGolden(tb, "g", got)
	if len(tb.failures) == 0 {
		t.Error("a golden with different text matched")
	}
}
