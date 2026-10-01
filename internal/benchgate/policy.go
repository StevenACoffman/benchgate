// Package benchgate is the pure core of the benchmark gate.
//
// It holds the vocabulary — benchmark series, comparisons, verdicts, coverage
// gaps, reports — and the transformations between them: parsing `go test
// -bench` output, deciding whether a change is a regression, classifying
// benchmark coverage gaps, and rendering a report. Every function here is a
// transformation of values, so none of it touches the filesystem, a
// subprocess, the clock or the network. Supplying those is the shell's job
// (summary_rules §5); the depguard rules in .golangci.yaml enforce it.
//
// Note on layout: every file in this package declares its constants before its
// types, because the house declaration order is const, var, type, func. Go
// resolves package-scope names regardless of order, so a typed constant may
// precede the type it is declared with.
package benchgate

import "slices"

// Defaults for every tunable in a Policy. They are exported so the flag
// definitions in cmd/ and the documentation share one source of truth rather
// than drifting apart, and so NewPolicy is purely a validator with no defaulting
// of its own to disagree with.
const (
	// DefaultAlpha is the p-value below which a change is called
	// statistically significant. 0.05 is benchstat's own default.
	DefaultAlpha = 0.05
	// DefaultTolerance is the percentage a significant change must exceed
	// before it counts as a regression. Statistical significance says a
	// difference is real; tolerance says it is worth acting on.
	DefaultTolerance = 5.0
	// DefaultConfidence is the level for the confidence interval reported
	// around each center estimate.
	DefaultConfidence = 0.95
	// DefaultMinCoverage is the benchmark-only statement coverage floor.
	// Zero means no floor: a project adopting the gate should see its real
	// number before being asked to clear a bar.
	DefaultMinCoverage = 0.0
)

// The possible Verdict values.
const (
	// VerdictRegressed means the change is both statistically significant and
	// larger than the tolerance, in the direction the unit calls worse.
	VerdictRegressed Verdict = "regressed"
	// VerdictImproved is VerdictRegressed's mirror: significant, over
	// tolerance, in the direction the unit calls better.
	VerdictImproved Verdict = "improved"
	// VerdictUnchanged covers both of the ways a change fails to be
	// actionable: the p-value did not clear alpha (so the difference is
	// indistinguishable from noise), or it did but the magnitude stayed
	// within tolerance (so the difference is real but immaterial). The
	// comparison always carries P and DeltaPercent, so which one applies is
	// visible in the report.
	VerdictUnchanged Verdict = "unchanged"
	// VerdictUnmeasurable means the statistics declined to answer — almost
	// always too few samples for the requested alpha. It is reported with the
	// underlying warning and never blocks: the gate has learned nothing, and
	// "no information" is not evidence of a regression.
	VerdictUnmeasurable Verdict = "unmeasurable"
	// VerdictAdded means the benchmark exists at the head revision only.
	// Usually a new benchmark, which is worth seeing.
	VerdictAdded Verdict = "added"
	// VerdictRemoved means the benchmark exists at the base revision only.
	// Either it was renamed or it was deleted, and a deleted benchmark is a
	// coverage gap that would otherwise go unmentioned.
	VerdictRemoved Verdict = "removed"
)

// Verdict is the outcome of comparing one metric of one benchmark between two
// revisions. It is a closed set, so the exhaustive linter finds every switch
// that forgets a case.
type Verdict string

// Significance is the statistical decision rule: when is a measured difference
// both real and worth acting on.
type Significance struct {
	// Alpha is the p-value cutoff for calling a difference significant.
	// Must be in (0, 1).
	Alpha float64
	// Tolerance is the percentage a significant difference must exceed to
	// count as a regression. Must be >= 0. Zero means any significant
	// difference counts.
	Tolerance float64
	// Confidence is the level of the confidence interval reported around each
	// center estimate. Must be in (0, 1).
	Confidence float64
}

// Coverage is the rule for how far benchmarks must reach into the code.
type Coverage struct {
	// MinPercent is the floor for benchmark-only statement coverage, as a
	// percentage. Zero disables the floor.
	MinPercent float64
	// RequirePerPackage reports every package that declares no benchmark at
	// all as a gap.
	RequirePerPackage bool
	// ReportUnreached reports every function the benchmarks never execute as
	// a gap. On a codebase with a lot of non-hot-path code this is noisy by
	// design — it is the list of things no benchmark can speak for.
	ReportUnreached bool
}

// Gates says which findings are allowed to fail the build. Both default to
// false: a gate that blocks before anyone has seen what it reports gets
// switched off rather than fixed.
type Gates struct {
	// Regression fails the build when any comparison is VerdictRegressed.
	Regression bool
	// Gap fails the build when any coverage gap is found.
	Gap bool
}

// Policy is a validated decision rule. Its fields are unexported and NewPolicy
// is the only constructor, so a Policy that exists is a Policy whose thresholds
// were checked — nothing downstream revalidates, and no function has to
// document what it does with a nonsense alpha (summary_rules §4).
type Policy struct {
	significance Significance
	coverage     Coverage
	gates        Gates
	metrics      []Metric
}

// NewPolicy validates the three rule groups and returns the Policy they
// describe.
//
// metrics restricts which units are gated on; an empty slice gates on every
// unit present in both result sets. Entries are tidied on the way in, so a
// caller may write the familiar "ns/op" and have it match the "sec/op" the
// benchmark reader produces.
//
// Requires: sig.Alpha and sig.Confidence in (0, 1); sig.Tolerance >= 0;
// cov.MinPercent in [0, 100].
// Ensures:  on nil error, the returned Policy satisfies every one of those
// bounds, and its metrics contain no duplicates and no empty entries.
func NewPolicy(sig Significance, cov Coverage, gates Gates, metrics []string) (Policy, error) {
	switch {
	case sig.Alpha <= 0 || sig.Alpha >= 1:
		return Policy{}, Errorf(EINVALID,
			"alpha must be between 0 and 1 exclusive, got %v", sig.Alpha)
	case sig.Confidence <= 0 || sig.Confidence >= 1:
		return Policy{}, Errorf(EINVALID,
			"confidence must be between 0 and 1 exclusive, got %v", sig.Confidence)
	case sig.Tolerance < 0:
		return Policy{}, Errorf(EINVALID,
			"tolerance must not be negative, got %v", sig.Tolerance)
	case cov.MinPercent < 0 || cov.MinPercent > 100:
		return Policy{}, Errorf(EINVALID,
			"minimum coverage must be a percentage between 0 and 100, got %v", cov.MinPercent)
	}
	tidied, err := tidyMetrics(metrics)
	if err != nil {
		return Policy{}, err
	}
	return Policy{significance: sig, coverage: cov, gates: gates, metrics: tidied}, nil
}

// Alpha returns the configured significance cutoff.
func (p Policy) Alpha() float64 { return p.significance.Alpha }

// Tolerance returns the configured regression threshold, as a percentage.
func (p Policy) Tolerance() float64 { return p.significance.Tolerance }

// Confidence returns the configured confidence level for interval estimates.
func (p Policy) Confidence() float64 { return p.significance.Confidence }

// Metrics returns the units this policy gates on, or an empty slice when it
// gates on all of them.
func (p Policy) Metrics() []Metric { return slices.Clone(p.metrics) }

// ChecksCoverage reports whether this policy asks for any coverage analysis at
// all. When it does not, the shell can skip the instrumented benchmark run
// entirely, which is the expensive half of a gate run.
func (p Policy) ChecksCoverage() bool {
	return p.coverage.MinPercent > 0 || p.coverage.RequirePerPackage || p.coverage.ReportUnreached
}

// Verdict classifies one measured difference.
//
// delta is the percentage change from base to head. better is the improvement
// direction for the unit: +1 when higher values are better, -1 when lower are,
// 0 when unknown. significant reports whether the p-value cleared alpha.
//
// An unknown direction never yields VerdictRegressed. The gate cannot tell a
// 40% rise in a custom unit from a 40% win, and guessing in the blocking
// direction would fail builds for improvements. Compare attaches a warning
// naming the unit, so the fix — a `better=` unit metadata line in the benchmark
// output — is discoverable.
func (p Policy) Verdict(delta float64, better int, significant bool) Verdict {
	if !significant || better == 0 {
		return VerdictUnchanged
	}
	if abs(delta) <= p.significance.Tolerance {
		return VerdictUnchanged
	}
	// A rise is worse for a lower-is-better unit, and better for the reverse.
	worse := (better < 0) == (delta > 0)
	if worse {
		return VerdictRegressed
	}
	return VerdictImproved
}

// gatesMetric reports whether this policy compares the given unit.
func (p Policy) gatesMetric(m Metric) bool {
	return len(p.metrics) == 0 || slices.Contains(p.metrics, m)
}

func (v Verdict) String() string { return string(v) }

func tidyMetrics(metrics []string) ([]Metric, error) {
	out := make([]Metric, 0, len(metrics))
	for _, raw := range metrics {
		m, err := ParseMetric(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
