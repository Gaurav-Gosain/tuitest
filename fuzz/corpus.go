package fuzz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Gaurav-Gosain/tuitest/tape"
)

// TapeFor renders a failure as a runnable tape file. The header records what
// broke and how to replay it; the body is the minimised input followed by an
// assertion that the failure did not happen. The result is an ordinary tape, so
// it runs under "tuitest run" with no fuzz-specific tooling and can be
// committed as a regression test.
//
// The trailing assertion is what makes the file a test rather than a
// transcript. Without it a reproduction passes even while the program still
// crashes, since replaying the input is not the same as checking the outcome,
// and "rerun with the same -corpus to check a fix" would be a promise the file
// could not keep.
func TapeFor(f *Failure) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s: %s\n", f.Kind, f.Detail)
	fmt.Fprintf(&b, "# found by tuitest fuzz at seed %d, iteration %d\n", f.Seed, f.Iteration)
	if f.Original > 0 {
		fmt.Fprintf(&b, "# minimised from %d commands to %d\n", f.Original, len(f.Commands))
	}
	if !f.Verified {
		b.WriteString("# warning: this reduction did not reproduce on confirmation, so the failure may be timing dependent\n")
	}
	b.WriteString("#\n")
	b.WriteString("# replay with: tuitest run <this file>\n")
	b.WriteString("\n")
	b.WriteString(tape.Sprint(f.Commands))
	if a := assertionFor(f); a != "" {
		b.WriteString(a)
	}

	if f.Screen != "" {
		b.WriteString("\n# --- screen at the point of failure ---\n")
		for _, line := range strings.Split(f.Screen, "\n") {
			b.WriteString("# " + line + "\n")
		}
	}
	return b.String()
}

// assertionFor renders the check that fails while the bug is present and passes
// once it is fixed. It returns "" for the kinds whose failure is not observable
// from inside a tape, rather than emitting an assertion that would always pass
// and read as a green regression test.
func assertionFor(f *Failure) string {
	switch f.Kind {
	case FailCrash, FailDirtyExit:
		// The program died abnormally, so asserting a clean exit fails for as
		// long as it does and passes once it survives the input. This adds no
		// input, so running the file reproduces exactly what the fuzzer drove.
		return assertionMarker + "# The bug: this program should still exit cleanly after the input above.\n" +
			"ExpectExit 0\n"
	default:
		// A hang, a screen inconsistency and memory growth are all judged by
		// the fuzzer from outside the tape, by watching the process. There is
		// no tape command that checks them without changing the reproduction
		// (a liveness probe means sending input the fuzzer did not send), so
		// the file stays a transcript and says so rather than carrying an
		// assertion that would pass either way.
		return assertionMarker + "# This failure is judged from outside the tape, by watching the process,\n" +
			"# so there is no assertion that \"tuitest run\" could check without\n" +
			"# changing the reproduction. The commands above are the input;\n" +
			"# rerun \"tuitest fuzz\" against the same corpus to check a fix.\n"
	}
}

// assertionMarker separates the reproduction from the assertion appended after
// it. Corpus replay truncates each entry here, so re-driving a saved failure
// sends exactly the input that was minimised and nothing else, while a person
// running "tuitest run" on the file still gets a real assertion.
const assertionMarker = "\n# --- assertion (not replayed by tuitest fuzz) ---\n"

// corpusName derives a stable filename from the failure kind and the minimised
// input, so re-finding the same bug overwrites its entry instead of piling up
// near-duplicates.
func corpusName(f *Failure) string {
	sum := sha256.Sum256([]byte(tape.Sprint(f.Commands)))
	return fmt.Sprintf("%s-%s.tape", f.Kind, hex.EncodeToString(sum[:4]))
}

// writeCorpus saves a reproduction into the corpus directory and returns its
// path.
func writeCorpus(dir string, f *Failure) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, corpusName(f))
	if err := os.WriteFile(path, []byte(TapeFor(f)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// replayCorpus runs every tape in the corpus directory and reports those that
// still fail. This is what turns found cases into a regression suite: a fix is
// confirmed when the corpus stops reproducing.
func replayCorpus(ctx context.Context, opts Options) ([]*Failure, error) {
	entries, err := os.ReadDir(opts.Corpus)
	if err != nil {
		if os.IsNotExist(err) {
			// An empty or absent corpus is the normal first-run state.
			return nil, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".tape" {
			continue
		}
		names = append(names, e.Name())
	}
	// Sort so a session's output is stable across runs.
	sort.Strings(names)

	var found []*Failure
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		path := filepath.Join(opts.Corpus, name)
		cmds, err := parseTapeFile(path)
		if err != nil {
			logf(opts.Out, "skipping corpus entry %s: %v\n", name, err)
			continue
		}
		if f := drive(ctx, opts, cmds); f != nil {
			f.Commands = cmds
			f.Detail = fmt.Sprintf("%s (corpus entry %s)", f.Detail, name)
			f.Verified = true
			found = append(found, f)
		}
	}
	return found, nil
}

// parseTapeFile reads a corpus entry, keeping only the reproduction. Everything
// from the assertion marker on is dropped, so replaying an entry drives the
// input that was minimised rather than the assertion written for human use.
func parseTapeFile(path string) ([]tape.Command, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, err
	}
	src := string(data)
	if i := strings.Index(src, strings.TrimPrefix(assertionMarker, "\n")); i >= 0 {
		src = src[:i]
	}
	return tape.Parse(strings.NewReader(src))
}

// LoadCorpusEntry reads a corpus entry the way a fuzzing session replays it:
// the reproduction only, with the trailing assertion dropped. It is exported so
// a test can check that the two halves of an entry stay separate.
func LoadCorpusEntry(path string) ([]tape.Command, error) { return parseTapeFile(path) }
