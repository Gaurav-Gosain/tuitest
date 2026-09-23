package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/fuzz"
)

// A finding replayed from the corpus was not generated from a seed, so the
// report names the entry instead of offering a seed. It used to print
// "reproduce with seed 0, iteration 0" for every replayed finding.
func TestFuzzReportNamesTheCorpusEntryOfAReplayedFinding(t *testing.T) {
	var out bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
	reportFuzz(env, &fuzz.Result{Failures: []*fuzz.Failure{{
		Kind:        fuzz.FailCrash,
		Detail:      "program exit status 2 (corpus entry crash-0a1b2c3d.tape)",
		CorpusEntry: "crash-0a1b2c3d.tape",
		Verified:    true,
	}}}, "corpus")

	got := out.String()
	if !strings.Contains(got, "replayed from corpus entry crash-0a1b2c3d.tape") {
		t.Errorf("the report should name the corpus entry:\n%s", got)
	}
	if strings.Contains(got, "--seed") {
		t.Errorf("the report offers a seed for a finding no seed produced:\n%s", got)
	}
}

// A generated finding's seed is the iteration's own, and the report has to
// say how to use it: as the session seed of a one-iteration run.
func TestFuzzReportSaysHowToUseTheSeed(t *testing.T) {
	var out bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
	reportFuzz(env, &fuzz.Result{Failures: []*fuzz.Failure{{
		Kind:      fuzz.FailCrash,
		Detail:    "program exit status 2",
		Seed:      12345,
		Iteration: 7,
		Verified:  true,
	}}}, "corpus")

	if got := out.String(); !strings.Contains(got, "--seed 12345 --iterations 1") {
		t.Errorf("the report should give the seed with --iterations 1:\n%s", got)
	}
}
