package benchgate_test

import (
	"errors"
	"fmt"
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

// errReadFailed is what failReader returns, so a test can assert the error
// survived whatever wrapping the parser applied.
var errReadFailed = errors.New("read failed")

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errReadFailed }

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

// `go tool cover` output is read alongside whatever else the go command
// printed, so a row whose last column is not a percentage has to be skipped
// rather than parsed into a zero that reads as an uncovered function.
func TestParseFuncCoverageSkipsRowsThatAreNotCoverage(t *testing.T) {
	t.Parallel()

	input := "" +
		"go: downloading example.com/thing v1.2.3\n" +
		"warning: no packages being tested depend on matches for pattern x\n" +
		"example.com/m/a.go:3:\tReal\t100.0%\n" +
		"example.com/m/a.go:9:\tNotAPercent\t12.5\n" + // no % suffix
		"example.com/m/a.go:11:\tAlsoNot\tabc%\n" + // not a number
		"short line\n" +
		"\n" +
		"total:\t\t(statements)\t100.0%\n"

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(input))
	ok(t, err)
	equals(t, 1, len(prof.Funcs))
	equals(t, "Real", prof.Funcs[0].Function)
	equals(t, 100.0, prof.Funcs[0].Percent)
	equals(t, true, prof.HasTotal)
	equals(t, 100.0, prof.TotalPercent)
}

func TestParseFuncCoverageReadsFractionalAndZeroPercentages(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader("" +
		"m/a.go:1:\tZero\t0.0%\n" +
		"m/a.go:2:\tFraction\t33.3%\n" +
		"m/a.go:3:\tFull\t100.0%\n" +
		"total:\t\t(statements)\t44.4%\n"))
	ok(t, err)
	equals(t, 3, len(prof.Funcs))
	equals(t, 0.0, prof.Funcs[0].Percent)
	equals(t, 33.3, prof.Funcs[1].Percent)
	equals(t, 100.0, prof.Funcs[2].Percent)
	equals(t, 44.4, prof.TotalPercent)
}

// Each per-package status line `go test` can print has to terminate that
// package's listing, or the names pile up and land on the wrong package.
func TestParseBenchmarkListHandlesEveryTerminator(t *testing.T) {
	t.Parallel()

	index, err := benchgate.ParseBenchmarkList(strings.NewReader("" +
		"BenchmarkOK\n" +
		"ok  \texample.com/m/ok\t0.1s\n" +
		"BenchmarkFailing\n" +
		"FAIL\texample.com/m/failing\t0.2s\n" +
		"?   \texample.com/m/notests\t[no test files]\n" +
		"BenchmarkLast\n" +
		"ok  \texample.com/m/last\t0.3s\n"))
	ok(t, err)
	equals(t, 4, len(index))
	equals(t, 1, len(index["example.com/m/ok"]))
	equals(t, "BenchmarkOK", index["example.com/m/ok"][0])
	// A failing package still declared its benchmarks, and they belong to it
	// rather than to whichever package is listed next.
	equals(t, 1, len(index["example.com/m/failing"]))
	equals(t, "BenchmarkFailing", index["example.com/m/failing"][0])
	equals(t, 0, len(index["example.com/m/notests"]))
	equals(t, 1, len(index["example.com/m/last"]))
	equals(t, "BenchmarkLast", index["example.com/m/last"][0])
}

func TestParseBenchmarkListIgnoresNonBenchmarkNames(t *testing.T) {
	t.Parallel()

	index, err := benchgate.ParseBenchmarkList(strings.NewReader("" +
		"TestSomething\n" + // -list matched a test, not a benchmark
		"FuzzSomething\n" +
		"BenchmarkReal\n" +
		"ok  \texample.com/m\t0.1s\n"))
	ok(t, err)
	equals(t, 1, len(index["example.com/m"]))
	equals(t, "BenchmarkReal", index["example.com/m"][0])
}

// Each gap kind reads differently, because each points the reader somewhere
// different: a package, a position in a file, or the whole module.
func TestGapString(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		gap  benchgate.Gap
		want string
	}{
		"below floor names no location": {
			gap:  benchgate.Gap{Kind: benchgate.GapBelowFloor, Detail: "below the floor"},
			want: "below the floor",
		},
		"no benchmark names the package": {
			gap: benchgate.Gap{
				Kind: benchgate.GapNoBenchmark, Package: "example.com/m/sub",
				Detail: "package declares no Benchmark function",
			},
			want: "example.com/m/sub: package declares no Benchmark function",
		},
		"unreached names the position and function": {
			gap: benchgate.Gap{
				Kind: benchgate.GapUnreached, Package: "example.com/m",
				Function: "Cold", Position: "example.com/m/a.go:5",
				Detail: "no benchmark executes any statement in this function",
			},
			want: "example.com/m/a.go:5: Cold: no benchmark executes any statement in this function",
		},
		"an unknown kind still says what it is": {
			gap:  benchgate.Gap{Kind: benchgate.GapKind("future-kind"), Detail: "something new"},
			want: "future-kind: something new",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			equals(t, tc.want, tc.gap.String())
		})
	}
}

// The floor is a boundary, so the behaviour on and around it has to be exact.
func TestFindGapsFloorBoundary(t *testing.T) {
	t.Parallel()

	profile := func(total float64) benchgate.CoverageProfile {
		t.Helper()
		prof, err := benchgate.ParseFuncCoverage(strings.NewReader(
			fmt.Sprintf("total:\t\t(statements)\t%.1f%%\n", total)))
		ok(t, err)
		return prof
	}

	cases := map[string]struct {
		total, floor float64
		wantGap      bool
	}{
		"well under the floor": {total: 10, floor: 80, wantGap: true},
		"just under the floor": {total: 79.9, floor: 80, wantGap: true},
		"exactly on the floor": {total: 80, floor: 80, wantGap: false},
		"just over the floor":  {total: 80.1, floor: 80, wantGap: false},
		"no floor configured":  {total: 0, floor: 0, wantGap: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPolicy(t, benchgate.Significance{},
				benchgate.Coverage{MinPercent: tc.floor}, benchgate.Gates{})
			got := benchgate.FindGaps(profile(tc.total), benchgate.BenchmarkIndex{}, p)
			if !tc.wantGap {
				equals(t, 0, len(got))
				return
			}
			equals(t, 1, len(got))
			equals(t, benchgate.GapBelowFloor, got[0].Kind)
			assert(t, strings.Contains(got[0].Detail, "floor"),
				"the detail should name the floor: "+got[0].Detail)
		})
	}
}

// A profile that carried no total at all means no coverage was gathered, which
// must not be mistaken for 0% and reported as a failed floor.
func TestFindGapsIgnoresTheFloorWithoutAProfile(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{},
		benchgate.Coverage{MinPercent: 80}, benchgate.Gates{})
	empty, err := benchgate.ParseFuncCoverage(strings.NewReader(""))
	ok(t, err)
	equals(t, false, empty.HasTotal)
	equals(t, 0, len(benchgate.FindGaps(empty, benchgate.BenchmarkIndex{}, p)))
}

// A row with fewer than three columns is not a coverage row, even when its last
// column parses as a percentage. Accepting it would invent a function with an
// empty position.
func TestParseFuncCoverageRejectsTooFewColumns(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader("" +
		"something 50.0%\n" + // two columns, last one a valid percentage
		"example.com/m/a.go:3:\tReal\t100.0%\n"))
	ok(t, err)
	equals(t, 1, len(prof.Funcs))
	equals(t, "Real", prof.Funcs[0].Function)
	equals(t, "example.com/m/a.go:3", prof.Funcs[0].Position)
}

// Only a function at exactly zero is unreached. One the benchmarks barely touch
// is a different finding, and reporting it as unreached would be false.
func TestFindGapsTreatsBarelyCoveredAsReached(t *testing.T) {
	t.Parallel()

	prof, err := benchgate.ParseFuncCoverage(strings.NewReader("" +
		"m/a.go:1:\tUntouched\t0.0%\n" +
		"m/a.go:2:\tBarely\t0.5%\n" +
		"m/a.go:3:\tPartly\t50.0%\n" +
		"total:\t\t(statements)\t16.8%\n"))
	ok(t, err)

	p := testPolicy(t, benchgate.Significance{},
		benchgate.Coverage{ReportUnreached: true}, benchgate.Gates{})
	got := benchgate.FindGaps(prof, benchgate.BenchmarkIndex{}, p)
	equals(t, 1, len(got))
	equals(t, "Untouched", got[0].Function)
}

// `go test` prefixes a failing test's output with "---", which terminates that
// package's listing just as "ok" does. A single-word line is not a terminator
// at all, and treating it as one would attribute names to an empty package.
func TestParseBenchmarkListTerminatorEdges(t *testing.T) {
	t.Parallel()

	index, err := benchgate.ParseBenchmarkList(strings.NewReader("" +
		"BenchmarkFirst\n" +
		"---\texample.com/m/dashes\n" +
		"ok\n" + // one field: not a terminator
		"BenchmarkSecond\n" +
		"ok  \texample.com/m/second\t0.1s\n"))
	ok(t, err)
	equals(t, 2, len(index))
	equals(t, 1, len(index["example.com/m/dashes"]))
	equals(t, "BenchmarkFirst", index["example.com/m/dashes"][0])
	equals(t, 1, len(index["example.com/m/second"]))
	equals(t, "BenchmarkSecond", index["example.com/m/second"][0])
	_, present := index[""]
	equals(t, false, present)
}

// Both parsers read from an io.Reader they do not own. A read failure has to
// surface, because a half-read profile silently reports fewer gaps than exist.
func TestParsersPropagateReadErrors(t *testing.T) {
	t.Parallel()

	_, err := benchgate.ParseFuncCoverage(failReader{})
	assert(t, err != nil, "a failing reader should produce an error")
	assert(t, errors.Is(err, errReadFailed), "the read error should survive wrapping")

	_, err = benchgate.ParseBenchmarkList(failReader{})
	assert(t, err != nil, "a failing reader should produce an error")
	assert(t, errors.Is(err, errReadFailed), "the read error should survive wrapping")
}
