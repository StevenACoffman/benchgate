package benchgate_test

import (
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
