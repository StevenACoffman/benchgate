package gate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
	"github.com/StevenACoffman/benchgate/internal/gotest"
)

// AnalyseCoverage runs the benchmarks with coverage instrumentation and reports
// what they fail to reach.
//
// Both `benchgate check` and `benchgate gaps` need exactly this, which is why it
// lives here rather than once in each: two copies of a four-subprocess sequence
// are two chances for the gate's answer and the report's answer to diverge.
//
// The instrumented measurements are discarded. Instrumentation changes them, so
// they are not comparable with anything, and only the set of executed
// statements is read. -count is forced to 1 for the same reason: coverage asks
// which statements ran, and running them again adds nothing.
//
// Requires: p was returned by benchgate.NewPolicy.
// Ensures:  no file is left behind; the returned gaps are classified by p, so
// they are the same findings its exit code will be computed from.
func AnalyseCoverage(
	ctx context.Context,
	runner gotest.Runner,
	opts *gotest.Options,
	p benchgate.Policy,
) (prof benchgate.CoverageProfile, gaps []benchgate.Gap, err error) {
	// A local copy, because forcing -count on the caller's options would change
	// how its uninstrumented runs are measured too.
	instrumented := *opts
	instrumented.Count = 1

	dir, err := os.MkdirTemp("", "benchgate-cover-")
	if err != nil {
		return prof, nil, fmt.Errorf("creating a coverage directory: %w", err)
	}
	defer func() { err = joinCleanup(err, os.RemoveAll(dir)) }()
	profilePath := filepath.Join(dir, "bench.cover")

	if _, err = runner.BenchWithCover(ctx, &instrumented, profilePath); err != nil {
		return prof, nil, fmt.Errorf("measuring benchmark coverage: %w", err)
	}
	coverOut, err := runner.CoverFunc(ctx, profilePath)
	if err != nil {
		return prof, nil, fmt.Errorf("reading the coverage profile: %w", err)
	}
	prof, err = benchgate.ParseFuncCoverage(strings.NewReader(string(coverOut)))
	if err != nil {
		return prof, nil, fmt.Errorf("parsing the coverage profile: %w", err)
	}

	listOut, err := runner.ListBenchmarks(ctx, instrumented.Packages)
	if err != nil {
		return prof, nil, fmt.Errorf("listing the benchmarks: %w", err)
	}
	index, err := benchgate.ParseBenchmarkList(strings.NewReader(string(listOut)))
	if err != nil {
		return prof, nil, fmt.Errorf("parsing the benchmark listing: %w", err)
	}
	return prof, benchgate.FindGaps(prof, index, p), nil
}

// joinCleanup reports a cleanup failure without letting it hide the real one. A
// leftover temporary directory is worth mentioning and is never the interesting
// error.
func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return fmt.Errorf("cleaning up: %w", cleanup)
	}
	return primary
}
