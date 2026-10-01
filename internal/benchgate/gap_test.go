package benchgate_test

import (
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
)

// Real `go tool cover -func` output, tabwriter padding and all.
const coverFuncOutput = "" +
	"example.com/m/a.go:3:\t\t\tHot\t\t100.0%\n" +
	"example.com/m/a.go:5:\t\t\tCold\t\t0.0%\n" +
	"example.com/m/a.go:7:\t\t\tNever\t\t0.0%\n" +
	"example.com/m/sub/sub.go:3:\tF\t\t40.0%\n" +
	"total:\t\t\t\t(statements)\t45.5%\n"

// Real `go test -list '^Benchmark' ./...` output: names, then a terminator line
// per package.
const benchListOutput = "" +
	"BenchmarkHot\n" +
	"BenchmarkHotParallel\n" +
	"ok  \texample.com/m\t0.274s\n" +
	"ok  \texample.com/m/nobench\t0.721s\n" +
	"?   \texample.com/m/sub\t[no test files]\n"

func TestParseFuncCoverage(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(coverFuncOutput))
	ok(t, err)
	equals(t, 4, len(prof.Funcs))
	equals(t, true, prof.HasTotal)
	equals(t, 45.5, prof.TotalPercent)

	equals(t, "example.com/m/a.go:3", prof.Funcs[0].Position)
	equals(t, "example.com/m", prof.Funcs[0].Package)
	equals(t, "Hot", prof.Funcs[0].Function)
	equals(t, 100.0, prof.Funcs[0].Percent)

	equals(t, "example.com/m/sub", prof.Funcs[3].Package)
	equals(t, "F", prof.Funcs[3].Function)
	equals(t, 40.0, prof.Funcs[3].Percent)
}

// A 0% total and a missing total are different facts: the first says benchmarks
// ran and touched nothing, the second says no profile was produced. HasTotal is
// what keeps them apart, so a missing profile cannot be read as a failed floor.
func TestParseFuncCoverageDistinguishesZeroFromAbsent(t *testing.T) {
	t.Parallel()

	empty, err := benchgate.ParseFuncCoverage(strings.NewReader(""))
	ok(t, err)
	equals(t, false, empty.HasTotal)
	equals(t, 0, len(empty.Funcs))

	zero, err := benchgate.ParseFuncCoverage(
		strings.NewReader("total:\t\t(statements)\t0.0%\n"))
	ok(t, err)
	equals(t, true, zero.HasTotal)
	equals(t, 0.0, zero.TotalPercent)
}

// Reading the columns from the right is what makes this work; a left-to-right
// split would attribute the path's second word as the function name.
func TestParseFuncCoverageToleratesSpacesInPaths(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(
		"example.com/m/my dir/a.go:3:\tHot\t100.0%\n"))
	ok(t, err)
	equals(t, 1, len(prof.Funcs))
	equals(t, "Hot", prof.Funcs[0].Function)
	equals(t, "example.com/m/my dir/a.go:3", prof.Funcs[0].Position)
}

func TestParseBenchmarkList(t *testing.T) {
	t.Parallel()

	index, err := benchgate.ParseBenchmarkList(strings.NewReader(benchListOutput))
	ok(t, err)
	equals(t, 3, len(index))
	equals(t, 2, len(index["example.com/m"]))
	equals(t, "BenchmarkHot", index["example.com/m"][0])
	equals(t, "BenchmarkHotParallel", index["example.com/m"][1])

	// Both a package with tests but no benchmarks and one with no test files at
	// all are present with no benchmarks, which is what makes them findable.
	benchmarks, present := index["example.com/m/nobench"]
	assert(t, present, "a package with tests but no benchmarks should be indexed")
	equals(t, 0, len(benchmarks))
	benchmarks, present = index["example.com/m/sub"]
	assert(t, present, "a package with no test files should be indexed")
	equals(t, 0, len(benchmarks))
}

func TestFindGaps(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(coverFuncOutput))
	ok(t, err)
	index, err := benchgate.ParseBenchmarkList(strings.NewReader(benchListOutput))
	ok(t, err)

	cases := map[string]struct {
		cov       benchgate.Coverage
		wantKinds []benchgate.GapKind
	}{
		"nothing requested finds nothing": {
			cov:       benchgate.Coverage{},
			wantKinds: nil,
		},
		"a cleared floor is not a gap": {
			cov:       benchgate.Coverage{MinPercent: 40},
			wantKinds: nil,
		},
		"a missed floor is one aggregate gap": {
			cov:       benchgate.Coverage{MinPercent: 80},
			wantKinds: []benchgate.GapKind{benchgate.GapBelowFloor},
		},
		"per-package finds the two bare packages, sorted": {
			cov: benchgate.Coverage{RequirePerPackage: true},
			wantKinds: []benchgate.GapKind{
				benchgate.GapNoBenchmark, benchgate.GapNoBenchmark,
			},
		},
		"unreached finds the two zero-percent functions": {
			cov: benchgate.Coverage{ReportUnreached: true},
			wantKinds: []benchgate.GapKind{
				benchgate.GapUnreached, benchgate.GapUnreached,
			},
		},
		"everything at once, floor first": {
			cov: benchgate.Coverage{
				MinPercent: 80, RequirePerPackage: true, ReportUnreached: true,
			},
			wantKinds: []benchgate.GapKind{
				benchgate.GapBelowFloor,
				benchgate.GapNoBenchmark, benchgate.GapNoBenchmark,
				benchgate.GapUnreached, benchgate.GapUnreached,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPolicy(t, benchgate.Significance{}, tc.cov, benchgate.Gates{})
			got := benchgate.FindGaps(prof, index, p)
			equals(t, len(tc.wantKinds), len(got))
			for i, want := range tc.wantKinds {
				equals(t, want, got[i].Kind)
			}
		})
	}
}

func TestFindGapsIsDeterministic(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(coverFuncOutput))
	ok(t, err)
	index, err := benchgate.ParseBenchmarkList(strings.NewReader(benchListOutput))
	ok(t, err)
	p := testPolicy(t, benchgate.Significance{},
		benchgate.Coverage{RequirePerPackage: true}, benchgate.Gates{})

	// Package names come out of a map, whose iteration order Go randomises, so
	// this would be flaky without the sort in noBenchmarkGaps. Repeating it
	// raises the chance of catching a reintroduced ordering bug.
	const repeats = 20
	want := renderGaps(benchgate.FindGaps(prof, index, p))
	for range repeats {
		equals(t, want, renderGaps(benchgate.FindGaps(prof, index, p)))
	}
	equals(t, "example.com/m/nobench: package declares no Benchmark function\n"+
		"example.com/m/sub: package declares no Benchmark function\n", want)
}

func TestExitCodeRequiresTheGateToBeOn(t *testing.T) {
	t.Parallel()

	tenFast := []float64{100, 101, 99, 100, 102, 98, 101, 100, 99, 100}
	tenSlow := []float64{130, 131, 129, 130, 132, 128, 131, 130, 129, 130}
	base := testSeries(t, benchOutput("BenchmarkX", tenFast...))
	head := testSeries(t, benchOutput("BenchmarkX", tenSlow...))

	cases := map[string]struct {
		gates    benchgate.Gates
		withGap  bool
		wantCode int
	}{
		"regression with no gate passes": {
			gates: benchgate.Gates{}, wantCode: benchgate.ExitPass,
		},
		"regression with the regression gate fails": {
			gates: benchgate.Gates{Regression: true}, wantCode: benchgate.ExitRegression,
		},
		"a gap with no gate passes": {
			gates: benchgate.Gates{}, withGap: true, wantCode: benchgate.ExitPass,
		},
		"a gap with the gap gate fails": {
			gates: benchgate.Gates{Gap: true}, withGap: true, wantCode: benchgate.ExitGap,
		},
		"a regression outranks a gap": {
			gates:   benchgate.Gates{Regression: true, Gap: true},
			withGap: true, wantCode: benchgate.ExitRegression,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPolicy(t, benchgate.Significance{Tolerance: 5},
				benchgate.Coverage{}, tc.gates)
			comparisons, err := benchgate.Compare(base, head, p)
			ok(t, err)

			report := benchgate.NewReport(p)
			report.Comparisons = comparisons
			if tc.withGap {
				report.Gaps = []benchgate.Gap{{
					Kind: benchgate.GapNoBenchmark, Package: "example.com/m/sub",
					Detail: "package declares no Benchmark function",
				}}
			}
			equals(t, tc.wantCode, report.ExitCode())
			equals(t, 1, len(report.Regressions()))
		})
	}
}

func renderGaps(gaps []benchgate.Gap) string {
	var b strings.Builder
	for i := range gaps {
		b.WriteString(gaps[i].String())
		b.WriteString("\n")
	}
	return b.String()
}
