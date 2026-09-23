package fuzz_test

import (
	"context"
	"testing"

	"github.com/Gaurav-Gosain/tuitest/fuzz"
)

// BenchmarkCampaign measures the throughput of a fuzzing session against the
// well-behaved fixture, with the default options a user gets from the CLI and
// shrinking off, since a session that finds nothing never shrinks. Nearly all
// of an iteration is waiting on the program rather than computing, so this is
// the number that moves when a wait inside an iteration changes, and ns/op is
// wall time per iteration.
//
// Each op is a whole session of a fixed seed, so every op drives the same
// input and a change in the result is a change in the harness, not in what it
// happened to generate.
func BenchmarkCampaign(b *testing.B) {
	const iterations = 10
	var total int
	for i := 0; i < b.N; i++ {
		opts := fuzz.DefaultOptions(argvFor("none"))
		opts.Seed = 5
		opts.Iterations = iterations
		opts.Shrink = false
		res, err := fuzz.Run(context.Background(), opts)
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Failures) != 0 {
			b.Fatalf("the well-behaved fixture produced findings: %v", res.Failures[0])
		}
		total += res.Iterations
	}
	b.ReportMetric(float64(total)/b.Elapsed().Seconds(), "iterations/s")
}
