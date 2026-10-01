package benchgate_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
)

func TestCompareVerdicts(t *testing.T) {
	t.Parallel()

	// Ten samples per arm is what a CI gate should collect, and is above
	// benchmath's minimum for a conclusive U-test at alpha 0.05.
	fast := []float64{100, 101, 99, 100, 102, 98, 101, 100, 99, 100}
	slow := []float64{130, 131, 129, 130, 132, 128, 131, 130, 129, 130}
	barelySlower := []float64{102, 103, 101, 102, 104, 100, 103, 102, 101, 102}

	cases := map[string]struct {
		base, head []float64
		tolerance  float64
		want       benchgate.Verdict
	}{
		"30% slower is a regression": {
			base: fast, head: slow, tolerance: 5, want: benchgate.VerdictRegressed,
		},
		"30% faster is an improvement": {
			base: slow, head: fast, tolerance: 5, want: benchgate.VerdictImproved,
		},
		"identical samples are unchanged": {
			base: fast, head: fast, tolerance: 5, want: benchgate.VerdictUnchanged,
		},
		// The defect the reference implementation has: a difference that is
		// statistically real but smaller than the tolerance must not fail a
		// build. 2% over a 5% tolerance is the case.
		"significant but under tolerance is unchanged": {
			base: fast, head: barelySlower, tolerance: 5, want: benchgate.VerdictUnchanged,
		},
		"the same difference regresses at a zero tolerance": {
			base: fast, head: barelySlower, tolerance: 0, want: benchgate.VerdictRegressed,
		},
		"too few samples is unmeasurable, not a regression": {
			base: []float64{100, 101}, head: []float64{130, 131},
			tolerance: 5, want: benchgate.VerdictUnmeasurable,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := testSeries(t, benchOutput("BenchmarkX", tc.base...))
			head := testSeries(t, benchOutput("BenchmarkX", tc.head...))
			p := testPolicy(t, benchgate.Significance{Tolerance: tc.tolerance},
				benchgate.Coverage{}, benchgate.Gates{})

			got, err := benchgate.Compare(base, head, p)
			ok(t, err)
			equals(t, 1, len(got))
			equals(t, tc.want, got[0].Verdict)
			equals(t, "ns/op", got[0].Unit)
			equals(t, benchgate.Metric("sec/op"), got[0].Metric)
			// Lower is better for a duration, which is what makes "slower" a
			// regression rather than an improvement.
			equals(t, -1, got[0].Better)
		})
	}
}

// A throughput metric is the case the reference implementation gets backwards:
// it treats any "+" delta as a degradation, so a 30% throughput win fails the
// build.
func TestCompareHonoursImprovementDirection(t *testing.T) {
	t.Parallel()

	output := func(mbPerSec ...float64) string {
		var b strings.Builder
		b.WriteString(benchHeader)
		for _, v := range mbPerSec {
			b.WriteString("BenchmarkThroughput-8\t1000\t10.00 ns/op\t")
			b.WriteString(num(v))
			b.WriteString(" MB/s\n")
		}
		return b.String()
	}

	base := testSeries(t, output(100, 101, 99, 100, 102, 98, 101, 100, 99, 100))
	head := testSeries(t, output(130, 131, 129, 130, 132, 128, 131, 130, 129, 130))
	p := testPolicy(t, benchgate.Significance{Tolerance: 5},
		benchgate.Coverage{}, benchgate.Gates{}, "MB/s")

	got, err := benchgate.Compare(base, head, p)
	ok(t, err)
	equals(t, 1, len(got))
	equals(t, benchgate.VerdictImproved, got[0].Verdict)
	equals(t, 1, got[0].Better)
	assert(t, got[0].DeltaPercent > 0, "a throughput rise should have a positive delta")
}

// A unit with no known direction must never be called a regression: guessing
// in the blocking direction would fail builds for improvements.
func TestCompareUnknownDirectionNeverRegresses(t *testing.T) {
	t.Parallel()

	output := func(vals ...float64) string {
		var b strings.Builder
		b.WriteString(benchHeader)
		for _, v := range vals {
			b.WriteString("BenchmarkCustom-8\t1000\t10.00 ns/op\t")
			b.WriteString(num(v))
			b.WriteString(" widgets/op\n")
		}
		return b.String()
	}

	base := testSeries(t, output(100, 101, 99, 100, 102, 98, 101, 100, 99, 100))
	head := testSeries(t, output(200, 201, 199, 200, 202, 198, 201, 200, 199, 200))
	p := testPolicy(t, benchgate.Significance{Tolerance: 5},
		benchgate.Coverage{}, benchgate.Gates{}, "widgets/op")

	got, err := benchgate.Compare(base, head, p)
	ok(t, err)
	equals(t, 1, len(got))
	equals(t, 0, got[0].Better)
	equals(t, benchgate.VerdictUnchanged, got[0].Verdict)
	assert(
		t,
		len(got[0].Warnings) > 0,
		"an unknown direction should warn, so the fix is discoverable",
	)
}

// A `better=` metadata line in the benchmark output is how a project teaches the
// gate about its own units, and honouring it is what makes the previous test's
// refusal recoverable rather than a dead end.
func TestCompareHonoursBetterMetadata(t *testing.T) {
	t.Parallel()

	output := func(vals ...float64) string {
		var b strings.Builder
		b.WriteString(benchHeader)
		b.WriteString("Unit widgets/op better=higher\n")
		for _, v := range vals {
			b.WriteString("BenchmarkCustom-8\t1000\t" + num(v) + " widgets/op\n")
		}
		return b.String()
	}

	base := testSeries(t, output(100, 101, 99, 100, 102, 98, 101, 100, 99, 100))
	head := testSeries(t, output(50, 51, 49, 50, 52, 48, 51, 50, 49, 50))
	p := testPolicy(t, benchgate.Significance{Tolerance: 5},
		benchgate.Coverage{}, benchgate.Gates{}, "widgets/op")

	got, err := benchgate.Compare(base, head, p)
	ok(t, err)
	equals(t, 1, len(got))
	equals(t, 1, got[0].Better)
	// Fewer widgets per op, where more is better, is a regression.
	equals(t, benchgate.VerdictRegressed, got[0].Verdict)
}

func TestCompareAddedAndRemoved(t *testing.T) {
	t.Parallel()

	ten := []float64{100, 101, 99, 100, 102, 98, 101, 100, 99, 100}
	base := testSeries(t, benchOutput("BenchmarkGone", ten...))
	head := testSeries(t, benchOutput("BenchmarkNew", ten...))
	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})

	got, err := benchgate.Compare(base, head, p)
	ok(t, err)
	equals(t, 2, len(got))

	// Head order first, then base-only, so output is deterministic.
	equals(t, benchgate.VerdictAdded, got[0].Verdict)
	equals(t, "BenchmarkNew-8", got[0].Key.Name)
	assert(t, got[0].HeadCenter > 0, "an added benchmark should report its head measurement")
	equals(t, 0.0, got[0].BaseCenter)
	equals(t, "-", got[0].DeltaString())
	equals(t, "-", got[0].PString())
	equals(t, "-", got[0].BaseCell())

	equals(t, benchgate.VerdictRemoved, got[1].Verdict)
	equals(t, "BenchmarkGone-8", got[1].Key.Name)
	assert(t, got[1].BaseCenter > 0, "a removed benchmark should report its base measurement")
	equals(t, 0.0, got[1].HeadCenter)
	equals(t, "-", got[1].HeadCell())
}

// Comparing measurements from two different machines yields a confident p-value
// for a meaningless question, so it is refused rather than reported.
func TestCompareRejectsMismatchedPlatforms(t *testing.T) {
	t.Parallel()

	ten := []float64{100, 101, 99, 100, 102, 98, 101, 100, 99, 100}
	base := testSeries(t, benchOutput("BenchmarkX", ten...))
	head := testSeries(t, strings.Replace(
		benchOutput("BenchmarkX", ten...), "goarch: amd64", "goarch: arm64", 1))
	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})

	_, err := benchgate.Compare(base, head, p)
	assert(t, err != nil, "mismatched platforms should be refused")
	equals(t, benchgate.EINVALID, benchgate.ErrorCode(err))
	assert(t, strings.Contains(err.Error(), "amd64"), "the message should name the platforms")
}

func TestCompareFiltersToSelectedMetrics(t *testing.T) {
	t.Parallel()

	output := benchHeader +
		strings.Repeat("BenchmarkX-8\t1000\t100.00 ns/op\t48 B/op\t2 allocs/op\n", 10)
	s := testSeries(t, output)

	all := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
	got, err := benchgate.Compare(s, s, all)
	ok(t, err)
	equals(t, 3, len(got))
	// The conventional print order, not Go's randomised map order.
	equals(t, benchgate.Metric("sec/op"), got[0].Metric)
	equals(t, benchgate.Metric("B/op"), got[1].Metric)
	equals(t, benchgate.Metric("allocs/op"), got[2].Metric)

	// "ns/op" as a human writes it must select the tidied "sec/op" metric.
	timeOnly := testPolicy(t, benchgate.Significance{},
		benchgate.Coverage{}, benchgate.Gates{}, "ns/op")
	got, err = benchgate.Compare(s, s, timeOnly)
	ok(t, err)
	equals(t, 1, len(got))
	equals(t, benchgate.Metric("sec/op"), got[0].Metric)
}

// benchmath.NewSample sorts the slice it is handed, in place. If Compare passed
// the Series' own slice, a second call would observe reordered data — so this
// asserts the clone is really there.
func TestCompareDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	// Deliberately unsorted, so an in-place sort would be visible.
	unsorted := []float64{130, 100, 120, 101, 125, 99, 121, 102, 124, 98}
	base := testSeries(t, benchOutput("BenchmarkX", unsorted...))
	head := testSeries(t, benchOutput("BenchmarkX", unsorted...))
	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})

	first, err := benchgate.Compare(base, head, p)
	ok(t, err)
	second, err := benchgate.Compare(base, head, p)
	ok(t, err)

	equals(t, len(first), len(second))
	for i := range first {
		equals(t, first[i].BaseCenter, second[i].BaseCenter)
		equals(t, first[i].HeadCenter, second[i].HeadCenter)
		equals(t, first[i].P, second[i].P)
		equals(t, first[i].Verdict, second[i].Verdict)
	}
}

// benchfmt reuses one *Result across Scan calls. A parser that retained the
// pointer would end up with every record showing the last one read, which shows
// up as a sample of one rather than of two.
func TestParseResultsCopiesEachRecord(t *testing.T) {
	t.Parallel()

	s := testSeries(t, benchOutput("BenchmarkX", 100, 200))
	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})

	got, err := benchgate.Compare(s, s, p)
	ok(t, err)
	equals(t, 1, len(got))
	equals(t, 2, got[0].N)
	// The median of two distinct values sits between them; a parser that kept
	// only the last record would report 200.
	assert(t, got[0].BaseCenter > 1e-7 && got[0].BaseCenter < 2e-7,
		"both records should contribute to the sample")
}

func TestParseResultsIgnoresNonBenchmarkLines(t *testing.T) {
	t.Parallel()

	noisy := "# example.com/m\n" + benchHeader +
		"BenchmarkX-8\t1000\t100.00 ns/op\n" +
		"PASS\nok  \texample.com/m\t1.234s\n"
	s := testSeries(t, noisy)
	equals(t, 1, s.Len())
	equals(t, "linux/amd64", s.Platform())
	equals(t, "Test CPU", s.CPU())
}

func TestParseResultsEmptyInputIsNotAnError(t *testing.T) {
	t.Parallel()

	s := testSeries(t, "")
	equals(t, 0, s.Len())
	equals(t, "", s.Platform())

	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
	got, err := benchgate.Compare(s, s, p)
	ok(t, err)
	equals(t, 0, len(got))
}

func TestNewPolicyRejectsOutOfRangeThresholds(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		sig     benchgate.Significance
		cov     benchgate.Coverage
		metrics []string
	}{
		"alpha zero":      {sig: benchgate.Significance{Alpha: 0, Confidence: 0.95}},
		"alpha one":       {sig: benchgate.Significance{Alpha: 1, Confidence: 0.95}},
		"alpha negative":  {sig: benchgate.Significance{Alpha: -0.1, Confidence: 0.95}},
		"confidence zero": {sig: benchgate.Significance{Alpha: 0.05, Confidence: 0}},
		"confidence one":  {sig: benchgate.Significance{Alpha: 0.05, Confidence: 1}},
		"negative tolerance": {
			sig: benchgate.Significance{Alpha: 0.05, Confidence: 0.95, Tolerance: -1},
		},
		"coverage over 100": {
			sig: benchgate.Significance{Alpha: 0.05, Confidence: 0.95},
			cov: benchgate.Coverage{MinPercent: 101},
		},
		"coverage negative": {
			sig: benchgate.Significance{Alpha: 0.05, Confidence: 0.95},
			cov: benchgate.Coverage{MinPercent: -1},
		},
		"empty metric": {
			sig:     benchgate.Significance{Alpha: 0.05, Confidence: 0.95},
			metrics: []string{"  "},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := benchgate.NewPolicy(tc.sig, tc.cov, benchgate.Gates{}, tc.metrics)
			assert(t, err != nil, "an out-of-range policy should be refused")
			equals(t, benchgate.EINVALID, benchgate.ErrorCode(err))
		})
	}
}

func TestNewPolicyDeduplicatesMetrics(t *testing.T) {
	t.Parallel()

	// "ns/op" and "sec/op" are the same metric once tidied, so the policy
	// should hold one entry, not two.
	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{},
		"ns/op", "sec/op", "B/op")
	got := p.Metrics()
	equals(t, 2, len(got))
	assert(t, slices.Contains(got, benchgate.Metric("sec/op")), "sec/op should be present")
	assert(t, slices.Contains(got, benchgate.Metric("B/op")), "B/op should be present")
}

func TestPolicyChecksCoverage(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		cov  benchgate.Coverage
		want bool
	}{
		"nothing requested": {cov: benchgate.Coverage{}, want: false},
		"floor only":        {cov: benchgate.Coverage{MinPercent: 50}, want: true},
		"per-package only":  {cov: benchgate.Coverage{RequirePerPackage: true}, want: true},
		"unreached only":    {cov: benchgate.Coverage{ReportUnreached: true}, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPolicy(t, benchgate.Significance{}, tc.cov, benchgate.Gates{})
			equals(t, tc.want, p.ChecksCoverage())
		})
	}
}

// Going from no allocations to some is the regression an allocation gate most
// needs to catch, and it is the one case where the percentage is undefined.
// Reporting 0% there, as a naive divide-by-zero guard does, clears every
// tolerance and lets the change through at --tolerance 0.
func TestCompareCatchesAMoveOffAZeroBaseline(t *testing.T) {
	t.Parallel()

	rows := func(allocs float64) string {
		var b strings.Builder
		b.WriteString(benchHeader)
		for range 8 {
			fmt.Fprintf(&b, "BenchmarkAlloc-8\t100\t10.00 ns/op\t%.0f allocs/op\n", allocs)
		}
		return b.String()
	}
	none, some := testSeries(t, rows(0)), testSeries(t, rows(5))
	p := testPolicy(t, benchgate.Significance{Tolerance: 0},
		benchgate.Coverage{}, benchgate.Gates{Regression: true}, "allocs/op")

	got, err := benchgate.Compare(none, some, p)
	ok(t, err)
	equals(t, 1, len(got))
	equals(t, benchgate.VerdictRegressed, got[0].Verdict)
	assert(t, math.IsInf(got[0].DeltaPercent, 1),
		"a move off zero has no finite percentage")
	equals(t, "+∞%", got[0].DeltaString())

	report := benchgate.NewReport(p)
	report.Comparisons = got
	equals(t, benchgate.ExitRegression, report.ExitCode())

	// The reverse is finite and is an improvement, not a second infinity.
	back, err := benchgate.Compare(some, none, p)
	ok(t, err)
	equals(t, benchgate.VerdictImproved, back[0].Verdict)
	equals(t, -100.0, back[0].DeltaPercent)
	equals(t, "-100.00%", back[0].DeltaString())

	// Zero to zero is genuinely no change rather than an undefined ratio.
	flat, err := benchgate.Compare(none, none, p)
	ok(t, err)
	equals(t, 0.0, flat[0].DeltaPercent)
	equals(t, benchgate.VerdictUnchanged, flat[0].Verdict)
}

// JSON cannot represent an infinity, and encoding one is an error rather than a
// quiet oddity, so the renderer has to omit the field instead.
func TestRenderJSONOmitsAnInfiniteDelta(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
	report := benchgate.NewReport(p)
	report.Base = benchgate.Revision{Ref: "main"}
	report.Head = benchgate.Revision{Ref: "HEAD"}
	report.Comparisons = []benchgate.Comparison{{
		Key: benchgate.Key{Name: "BenchmarkAlloc"}, Metric: "allocs/op", Unit: "allocs/op",
		Verdict: benchgate.VerdictRegressed, DeltaPercent: math.Inf(1),
		BaseCenter: 0, HeadCenter: 5, Better: -1,
	}}

	var buf bytes.Buffer
	ok(t, benchgate.RenderJSON(&buf, &report))

	var decoded struct {
		Comparisons []map[string]any `json:"comparisons"`
	}
	ok(t, json.Unmarshal(buf.Bytes(), &decoded))
	equals(t, 1, len(decoded.Comparisons))
	_, present := decoded.Comparisons[0]["delta_percent"]
	equals(t, false, present)
	equals(t, 5.0, decoded.Comparisons[0]["head_center"])
}

// sameValues decides whether a comparison is certainly unchanged or merely
// untestable, so each way of differing has to be distinguishable.
func TestCompareIdenticalSamplesVersusConstantDifference(t *testing.T) {
	t.Parallel()

	rows := func(values ...float64) string {
		var b strings.Builder
		b.WriteString(benchHeader)
		for _, v := range values {
			fmt.Fprintf(&b, "BenchmarkX-8\t100\t10.00 ns/op\t%.0f allocs/op\n", v)
		}
		return b.String()
	}
	const n = 8
	constant := func(v float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	varying := append(constant(1)[:n-1], 2)

	cases := map[string]struct {
		base, head []float64
		want       benchgate.Verdict
		wantWarn   bool
	}{
		"both constant and equal": {
			base: constant(1),
			head: constant(1),
			want: benchgate.VerdictUnchanged,
		},
		"both constant but different": {
			base: constant(1),
			head: constant(8),
			want: benchgate.VerdictRegressed,
		},
		"base varies, head constant": {
			base:     varying,
			head:     constant(1),
			want:     benchgate.VerdictUnchanged,
			wantWarn: true,
		},
		"base constant, head varies": {
			base:     constant(1),
			head:     varying,
			want:     benchgate.VerdictUnchanged,
			wantWarn: true,
		},
		"constant head below constant": {
			base: constant(8),
			head: constant(1),
			want: benchgate.VerdictImproved,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPolicy(t, benchgate.Significance{Tolerance: 0},
				benchgate.Coverage{}, benchgate.Gates{}, "allocs/op")
			got, err := benchgate.Compare(
				testSeries(t, rows(tc.base...)), testSeries(t, rows(tc.head...)), p)
			ok(t, err)
			equals(t, 1, len(got))
			equals(t, tc.want, got[0].Verdict)
			// Identical samples are certainly unchanged, so the statistics'
			// "cannot test this" warnings must not be reported as doubt.
			if !tc.wantWarn {
				equals(t, 0, len(got[0].Warnings))
			}
		})
	}
}

// The base and head summaries warn independently and word the common case
// identically, so the same sentence must not appear twice on one row.
func TestCompareDeduplicatesWarnings(t *testing.T) {
	t.Parallel()

	three := []float64{100, 101, 99}
	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
	got, err := benchgate.Compare(
		testSeries(t, benchOutput("BenchmarkX", three...)),
		testSeries(t, benchOutput("BenchmarkX", three...)), p)
	ok(t, err)
	equals(t, 1, len(got))

	seen := make(map[string]int, len(got[0].Warnings))
	for _, w := range got[0].Warnings {
		seen[w]++
	}
	for msg, count := range seen {
		equals(t, 1, count)
		assert(t, msg != "", "a warning should carry a message")
	}
	assert(t, len(got[0].Warnings) > 0, "three samples should warn about sample size")
}

// An unstable measurement produces a confident verdict from data that cannot
// support one, which is the failure mode a performance gate is most often
// wrong about.
func TestCompareWarnsAboutUnstableMeasurements(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})

	steady := []float64{100, 100, 101, 100, 99, 100, 100, 101}
	wild := []float64{10, 500, 20, 900, 15, 700, 30, 1100}

	stable, err := benchgate.Compare(
		testSeries(t, benchOutput("BenchmarkX", steady...)),
		testSeries(t, benchOutput("BenchmarkX", steady...)), p)
	ok(t, err)
	for _, w := range stable[0].Warnings {
		assert(t, !strings.Contains(w, "varied by"),
			"a steady measurement should not be called unstable: "+w)
	}

	noisy, err := benchgate.Compare(
		testSeries(t, benchOutput("BenchmarkX", wild...)),
		testSeries(t, benchOutput("BenchmarkX", wild...)), p)
	ok(t, err)
	var mentions int
	for _, w := range noisy[0].Warnings {
		if strings.Contains(w, "varied by") {
			mentions++
			assert(t, strings.Contains(w, "--rounds"), "the warning should name the fix: "+w)
		}
	}
	// One per side: the base and the head are each unstable here.
	equals(t, 2, mentions)
}

// One empty side is not an error. The caller decides whether "nothing ran"
// matters, and every benchmark then reports as added or removed.
func TestCompareWithOneEmptySide(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
	populated := testSeries(t, benchOutput("BenchmarkX", 100, 101, 99))
	empty := testSeries(t, "")

	added, err := benchgate.Compare(empty, populated, p)
	ok(t, err)
	equals(t, 1, len(added))
	equals(t, benchgate.VerdictAdded, added[0].Verdict)

	removed, err := benchgate.Compare(populated, empty, p)
	ok(t, err)
	equals(t, 1, len(removed))
	equals(t, benchgate.VerdictRemoved, removed[0].Verdict)
}

// A custom unit may carry negative values, and a move off a zero baseline then
// has to run to minus infinity rather than plus. Getting the sign wrong would
// turn a regression into an improvement.
func TestCompareSignsAnInfiniteDeltaByDirection(t *testing.T) {
	t.Parallel()

	rows := func(v float64) string {
		var b strings.Builder
		b.WriteString(benchHeader)
		b.WriteString("Unit widgets/op better=higher\n")
		for range 8 {
			fmt.Fprintf(&b, "BenchmarkX-8\t100\t%.2f widgets/op\n", v)
		}
		return b.String()
	}
	p := testPolicy(t, benchgate.Significance{Tolerance: 0},
		benchgate.Coverage{}, benchgate.Gates{}, "widgets/op")

	// More widgets is better, so zero to a negative count is a regression that
	// runs to minus infinity.
	down, err := benchgate.Compare(testSeries(t, rows(0)), testSeries(t, rows(-5)), p)
	ok(t, err)
	equals(t, 1, len(down))
	assert(t, math.IsInf(down[0].DeltaPercent, -1),
		"zero to a negative value is minus infinity, got "+down[0].DeltaString())
	equals(t, "-∞%", down[0].DeltaString())
	equals(t, benchgate.VerdictRegressed, down[0].Verdict)

	// And the mirror: zero to a positive count is plus infinity, an improvement
	// for this unit.
	up, err := benchgate.Compare(testSeries(t, rows(0)), testSeries(t, rows(5)), p)
	ok(t, err)
	assert(t, math.IsInf(up[0].DeltaPercent, 1), "zero to a positive value is plus infinity")
	equals(t, "+∞%", up[0].DeltaString())
	equals(t, benchgate.VerdictImproved, up[0].Verdict)
}
