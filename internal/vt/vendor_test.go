package vt_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// upstreamRecord parses internal/vt/UPSTREAM, the file that records which tuios
// revision this copy was taken from.
func upstreamRecord(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("UPSTREAM")
	if err != nil {
		t.Fatalf("reading the vendoring record: %v", err)
	}
	rec := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			rec[key] = value
		}
	}
	return rec
}

// TestUpstreamRecordIsWellFormed guards the provenance of the vendored copy. A
// copy whose upstream revision is unknown cannot be diffed against upstream,
// which is the only thing that keeps it from silently rotting.
func TestUpstreamRecordIsWellFormed(t *testing.T) {
	rec := upstreamRecord(t)
	for _, key := range []string{"repo", "path", "commit", "date"} {
		if rec[key] == "" {
			t.Errorf("UPSTREAM is missing %q", key)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(rec["commit"]) {
		t.Errorf("UPSTREAM commit %q is not a full git hash", rec["commit"])
	}
	if _, err := os.Stat("VENDOR.md"); err != nil {
		t.Errorf("the vendoring policy is missing: %v", err)
	}
}

// TestVendoredCopyMatchesUpstream is the drift check. Point TUITEST_TUIOS_SRC at
// a tuios checkout and every vendored file must be byte-identical to the commit
// recorded in UPSTREAM. Without the checkout there is nothing to compare
// against, so it skips; with it, a local edit or a stale record fails.
func TestVendoredCopyMatchesUpstream(t *testing.T) {
	src := os.Getenv("TUITEST_TUIOS_SRC")
	if src == "" {
		t.Skip("set TUITEST_TUIOS_SRC to a tuios checkout to check for vendor drift")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	rec := upstreamRecord(t)
	local, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, path := range local {
		base := filepath.Base(path)
		// doc.go and the tests belong to tuitest, not to upstream.
		if base == "doc.go" || strings.HasSuffix(base, "_test.go") {
			continue
		}
		want, err := exec.Command("git", "-C", src, "show", rec["commit"]+":"+rec["path"]+"/"+base).Output()
		if err != nil {
			t.Errorf("%s: not present at upstream %s: %v", base, rec["commit"][:12], err)
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s differs from upstream %s; fix it in tuios and re-run scripts/vendor-vt.sh",
				base, rec["commit"][:12])
		}
		checked++
	}
	if checked == 0 {
		t.Error("no vendored files were checked; the glob or the record is wrong")
	}
}
