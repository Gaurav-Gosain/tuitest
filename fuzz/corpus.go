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
// broke and how to replay it; the body is the minimised input. The result is an
// ordinary tape, so it runs under "tuitest run" with no fuzz-specific tooling
// and can be committed as a regression test.
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

	if f.Screen != "" {
		b.WriteString("\n# --- screen at the point of failure ---\n")
		for _, line := range strings.Split(f.Screen, "\n") {
			b.WriteString("# " + line + "\n")
		}
	}
	return b.String()
}

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

func parseTapeFile(path string) ([]tape.Command, error) {
	file, err := os.Open(path) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	return tape.Parse(file)
}
