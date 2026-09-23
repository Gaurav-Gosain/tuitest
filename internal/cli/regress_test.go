package cli

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
)

// A binary built by "go install module@version" has no -ldflags stamp, so the
// version has to come from the build info the toolchain recorded. Verified to
// fail: returning Version unchanged reports "dev" for the installed case.
func TestVersionFallsBackToTheModuleVersion(t *testing.T) {
	installed := &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}}
	checkout := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	cases := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"go install", "dev", installed, "v0.4.0"},
		{"ldflags stamp wins", "v9.9.9", installed, "v9.9.9"},
		{"go build in a checkout", "dev", checkout, "dev"},
		{"no build info", "dev", nil, "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.stamped, c.info); got != c.want {
				t.Fatalf("resolveVersion(%q) = %q, want %q", c.stamped, got, c.want)
			}
		})
	}
}

// A fuzz run with no bound is a mistake on the command line, so it is a usage
// error, not the harness error fuzz.Run's own rejection classified as.
// Verified to fail: without the check in fuzzCommand this exits 3.
func TestFuzzUnboundedRunIsUsageError(t *testing.T) {
	code, _, stderr := runCLI(nil, "fuzz", "--iterations", "0", "--", "true")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--duration") {
		t.Errorf("message does not say how to bound the run:\n%s", stderr)
	}
}

// An --exclude token the fuzzer can never match used to be accepted and
// excluded nothing. Verified to fail: without the ResolveKey check in the fuzz
// command these exit 0.
func TestFuzzRejectsExcludeTokensThatAreNotKeys(t *testing.T) {
	for _, tok := range []string{"ctrl+c", "Bogus+x", "esc"} {
		code, _, stderr := runCLI(nil, "fuzz", "--iterations", "1", "--exclude", "Ctrl+q,"+tok, "--", "true")
		if code != ExitUsage {
			t.Errorf("--exclude %s: exit code = %d, want %d; stderr:\n%s", tok, code, ExitUsage, stderr)
			continue
		}
		if !strings.Contains(stderr, tok) {
			t.Errorf("--exclude %s: message does not name the token:\n%s", tok, stderr)
		}
	}
}

// The JSON error is the same text the plain output prints. It used to be the
// raw error, which repeated the "tuitest: " prefix inside a line error.
// Verified to fail: setting res.Error from err.Error() leaves the prefix in.
func TestRunJSONErrorMatchesThePlainReport(t *testing.T) {
	path := writeTape(t, "Set Size 40 10\nSpawn "+echoBin+"\nWait /neverappears/ @300ms\n")

	code, stdout, _ := runCLI(nil, "run", "--json", path)
	if code != ExitTimeout {
		t.Fatalf("exit code = %d, want %d", code, ExitTimeout)
	}
	var res runResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if !strings.HasPrefix(res.Error, "tape line 3: WaitForMatch timed out") {
		t.Errorf("error = %q, want it to start with the rendered line error", res.Error)
	}
	if strings.Contains(res.Error, "tuitest: ") {
		t.Errorf("error repeats the tool prefix: %q", res.Error)
	}
}
