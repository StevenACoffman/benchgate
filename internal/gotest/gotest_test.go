package gotest_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
	"github.com/StevenACoffman/benchgate/internal/gotest"
)

// The go toolchain is running this test, so it is present by definition. The
// guard is here for the sake of a cross-compiled or stripped environment, and
// because summary_rules §10 asks for a guard rather than a hardcoded path.
func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found on PATH")
	}
}

// testModule is the throwaway module under testdata. go test sets the working
// directory to the package directory, so a relative path is all that is needed.
func testModule(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "mod")
}

func TestOptionsArgs(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		opts gotest.Options
		want []string
	}{
		"the zero value is a complete benchmark-only run": {
			opts: gotest.Options{},
			want: []string{"test", "./...", "-run", "^$", "-bench", ".", "-count", "1"},
		},
		"every field appears in go's own order": {
			opts: gotest.Options{
				Packages: "./internal/hot", Bench: "^BenchmarkX$", Count: 4,
				Benchtime: "100x", CPU: "1,4", Tags: "integration", Benchmem: true,
			},
			want: []string{
				"test", "./internal/hot", "-run", "^$", "-bench", "^BenchmarkX$",
				"-count", "4", "-benchtime", "100x", "-cpu", "1,4",
				"-tags", "integration", "-benchmem",
			},
		},
		"a non-positive count falls back to the default": {
			opts: gotest.Options{Count: -1},
			want: []string{"test", "./...", "-run", "^$", "-bench", ".", "-count", "1"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := tc.opts.Args()
			if !slices.Equal(tc.want, got) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

// Without -coverpkg a benchmark instruments only its own package, so any
// coverage floor over a multi-package module is unmeetable. That is the defect
// in the reference implementation, and this is the assertion that keeps the fix.
func TestOptionsCoverArgsAlwaysSetsCoverpkg(t *testing.T) {
	t.Parallel()

	opts := gotest.Options{Packages: "./..."}
	got := opts.CoverArgs("/tmp/profile.out")

	i := slices.Index(got, "-coverpkg")
	if i < 0 || i+1 >= len(got) {
		t.Fatalf("expected -coverpkg with a value, got %v", got)
	}
	if got[i+1] != "./..." {
		t.Fatalf("expected -coverpkg ./..., got %q", got[i+1])
	}
	if !slices.Contains(got, "-coverprofile") {
		t.Fatalf("expected -coverprofile, got %v", got)
	}
	// The base arguments must survive: an instrumented run is still a
	// benchmark-only run.
	if !slices.Contains(got, "-bench") || !slices.Contains(got, "^$") {
		t.Fatalf("expected the benchmark-only arguments to be preserved, got %v", got)
	}
}

func TestOptionsStringIsReproducible(t *testing.T) {
	t.Parallel()

	opts := gotest.Options{Count: 6, Benchmem: true}
	equals(t, "go test ./... -run ^$ -bench . -count 6 -benchmem", opts.String())
}

// The parser in internal/benchgate is fed by this runner, so the two are
// exercised together against real toolchain output rather than against a
// fixture that could drift from it.
func TestRunnerBenchProducesParseableOutput(t *testing.T) {
	t.Parallel()
	requireGo(t)

	r := gotest.Runner{Dir: testModule(t)}
	// -benchtime 1x keeps the suite fast: the gate's statistics come from
	// repeated rounds, and this test is about the plumbing, not the numbers.
	opts := gotest.Options{Benchtime: "1x", Benchmem: true}

	out, err := r.Bench(t.Context(), &opts)
	ok(t, err)

	series, err := benchgate.ParseResults(strings.NewReader(string(out)), "bench")
	ok(t, err)
	equals(t, 1, series.Len())
	assert(t, series.Platform() != "", "real output should carry goos and goarch")
	assert(t, series.CPU() != "", "real output should carry a cpu line")
	equals(t, "BenchmarkSum-"+gomaxprocsSuffix(series), series.Keys()[0].Name)
}

func TestRunnerListBenchmarksIndexesEveryPackage(t *testing.T) {
	t.Parallel()
	requireGo(t)

	r := gotest.Runner{Dir: testModule(t)}
	out, err := r.ListBenchmarks(t.Context(), "./...")
	ok(t, err)

	index, err := benchgate.ParseBenchmarkList(strings.NewReader(string(out)))
	ok(t, err)

	equals(t, 1, len(index["benchgate.test/mod"]))
	equals(t, "BenchmarkSum", index["benchgate.test/mod"][0])
	// The package with no benchmarks must be present with none, not absent:
	// that is what makes it reportable as a gap.
	benchmarks, present := index["benchgate.test/mod/sub"]
	assert(t, present, "a package with no benchmarks should still be indexed")
	equals(t, 0, len(benchmarks))
}

func TestRunnerCoverFuncFindsTheUnreachedFunction(t *testing.T) {
	t.Parallel()
	requireGo(t)

	r := gotest.Runner{Dir: testModule(t)}
	opts := gotest.Options{Benchtime: "1x"}
	// t.TempDir is cleaned up for us, and keeping the profile out of the module
	// directory means a failed run cannot leave the tree dirty.
	profile := filepath.Join(t.TempDir(), "bench.out")

	_, err := r.BenchWithCover(t.Context(), &opts, profile)
	ok(t, err)
	out, err := r.CoverFunc(t.Context(), profile)
	ok(t, err)

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(string(out)))
	ok(t, err)
	assert(t, prof.HasTotal, "a real profile should carry a total")

	byName := make(map[string]float64, len(prof.Funcs))
	for _, fn := range prof.Funcs {
		byName[fn.Function] = fn.Percent
	}
	assert(t, byName["Sum"] > 0, "the benchmarked function should be covered")
	equals(t, 0.0, byName["Unreached"])
	// -coverpkg reaches the sibling package, which is the whole point of it.
	equals(t, 0.0, byName["Idle"])
}

func TestRunnerReportsAFailedCommand(t *testing.T) {
	t.Parallel()
	requireGo(t)

	r := gotest.Runner{Dir: testModule(t)}
	// A package pattern that matches nothing in this module.
	opts := gotest.Options{Packages: "./no-such-package"}

	_, err := r.Bench(t.Context(), &opts)
	assert(t, err != nil, "a failing go command should be an error")
	// The message must carry both the command line and go's own diagnostic, or
	// a CI log shows a bare exit status and nobody can act on it.
	assert(t, strings.Contains(err.Error(), "no-such-package"),
		"the error should name the command that failed: "+err.Error())
}

func TestRunnerHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	requireGo(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // already cancelled: the command must not run to completion

	r := gotest.Runner{Dir: testModule(t)}
	opts := gotest.Options{Benchtime: "1x"}
	_, err := r.Bench(ctx, &opts)
	assert(t, err != nil, "a cancelled context should abort the run")
}

// gomaxprocsSuffix recovers the -N suffix go appended to the benchmark name, so
// the assertion does not depend on the machine's CPU count.
func gomaxprocsSuffix(series benchgate.Series) string {
	name := series.Keys()[0].Name
	_, suffix, _ := strings.Cut(name, "BenchmarkSum-")
	return suffix
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
}

func assert(t *testing.T, condition bool, msg string) {
	t.Helper()
	if !condition {
		t.Fatal(msg)
	}
}

func equals(t *testing.T, exp, act any) {
	t.Helper()
	if exp != act {
		t.Fatalf("expected %v, got %v", exp, act)
	}
}
