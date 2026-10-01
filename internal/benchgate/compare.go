package benchgate

import (
	"fmt"
	"math"
	"slices"

	"golang.org/x/perf/benchmath"
)

// UnstableRangePercent is the confidence-interval half-width, as a percentage
// of the center, above which a measurement is too noisy for its verdict to
// mean much.
//
// There is deliberately no flag for it. It does not change any decision — a
// wide interval attaches a warning and nothing else — so exposing it would only
// let an operator turn off a diagnostic, and the number a reader needs is the
// range itself, which is printed next to every measurement either way.
const UnstableRangePercent = 10.0

// Comparison is the result of comparing one metric of one benchmark between two
// revisions.
//
// Every field is populated for every verdict that has a measurement on both
// sides, so a reader can always see why a verdict was reached: whether the
// p-value or the tolerance was the deciding constraint.
type Comparison struct {
	// Key identifies the benchmark, including the platform it ran on.
	Key Key
	// Metric is the tidied unit, and Unit the spelling the benchmark printed.
	Metric Metric
	Unit   string
	// Verdict is the classification. See the Verdict constants.
	Verdict Verdict
	// BaseCenter and HeadCenter are the center estimates — the median, under
	// the default distribution-free assumption — in Metric's units.
	BaseCenter float64
	HeadCenter float64
	// BaseLo, BaseHi, HeadLo and HeadHi bound the confidence interval around
	// each center, at the policy's confidence level.
	BaseLo, BaseHi float64
	HeadLo, HeadHi float64
	// DeltaPercent is the percentage change from BaseCenter to HeadCenter.
	// Positive means the number went up, which is not the same as worse — see
	// Better.
	DeltaPercent float64
	// P is the p-value of the null hypothesis that both samples come from the
	// same distribution. Zero means the test was exact.
	P float64
	// Alpha is the cutoff P was judged against, copied from the policy so a
	// serialised comparison is self-describing.
	Alpha float64
	// N and NHead are the sample counts on each side.
	N, NHead int
	// Better is the improvement direction for Metric: +1 if higher values are
	// better, -1 if lower are, 0 if unknown.
	Better int
	// Warnings carries anything the statistics wanted to say — most often that
	// the sample is too small for the requested alpha.
	Warnings []string
}

// Compare pairs the benchmarks in base and head and classifies every metric of
// every pair according to p.
//
// Benchmarks present on one side only are reported as VerdictAdded or
// VerdictRemoved rather than skipped or treated as errors: a new benchmark and
// a deleted one are both things a reviewer should see (summary_rules §3,
// defining errors out of existence).
//
// Requires: p was returned by NewPolicy.
// Ensures:  the result is ordered by head's read order, then base's, so output
// is deterministic; neither base's nor head's stored values are modified; and
// EINVALID is returned when the two sets were measured on different platforms.
func Compare(base, head Series, p Policy) ([]Comparison, error) {
	if err := checkComparable(base, head); err != nil {
		return nil, err
	}
	th := benchmath.Thresholds{CompareAlpha: p.significance.Alpha}
	out := make([]Comparison, 0, head.Len()+base.Len())
	for _, k := range head.keys {
		if _, inBase := base.values[k]; !inBase {
			out = append(out, head.oneSided(k, VerdictAdded, p)...)
			continue
		}
		out = append(out, comparePair(base, head, k, p, &th)...)
	}
	for _, k := range base.keys {
		if _, inHead := head.values[k]; !inHead {
			out = append(out, base.oneSided(k, VerdictRemoved, p)...)
		}
	}
	return out, nil
}

// checkComparable rejects two result sets that cannot meaningfully be compared.
//
// Mismatched platforms are operator error — the wrong pair of files was handed
// over — and because Key includes the platform, letting it through would report
// every benchmark as both added and removed. Saying so plainly is more useful
// than a report the reader has to decode.
func checkComparable(base, head Series) error {
	bp, hp := base.Platform(), head.Platform()
	if bp == "" || hp == "" {
		return nil // one side is empty; the caller decides whether that is fatal
	}
	if bp != hp {
		return Errorf(
			EINVALID,
			"cannot compare results measured on different platforms: base is %s, head is %s",
			bp,
			hp,
		)
	}
	return nil
}

// comparePair runs the statistics for every gated metric the two sides share.
func comparePair(base, head Series, k Key, p Policy, th *benchmath.Thresholds) []Comparison {
	out := make([]Comparison, 0, len(head.values[k]))
	for _, m := range head.metrics(k) {
		if !p.gatesMetric(m) {
			continue
		}
		if _, ok := base.values[k][m]; !ok {
			continue // the unit appeared only at head; nothing to compare against
		}
		out = append(out, compareMetric(base, head, k, m, p, th))
	}
	return out
}

func compareMetric(
	base, head Series,
	k Key,
	m Metric,
	p Policy,
	th *benchmath.Thresholds,
) Comparison {
	// The assumption and direction come from the head run's unit metadata, so a
	// benchmark that declares `better=higher` for a custom unit is honoured.
	assumption := head.units.GetAssumption(string(m))
	better := head.units.GetBetter(string(m))
	conf := p.significance.Confidence

	baseSample := base.sample(k, m, th)
	headSample := head.sample(k, m, th)
	baseSummary := assumption.Summary(baseSample, conf)
	headSummary := assumption.Summary(headSample, conf)
	cmp := assumption.Compare(baseSample, headSample)

	c := Comparison{
		Key:          k,
		Metric:       m,
		Unit:         head.displayUnit(m),
		BaseCenter:   baseSummary.Center,
		HeadCenter:   headSummary.Center,
		BaseLo:       baseSummary.Lo,
		BaseHi:       baseSummary.Hi,
		HeadLo:       headSummary.Lo,
		HeadHi:       headSummary.Hi,
		DeltaPercent: percentChange(baseSummary.Center, headSummary.Center),
		P:            cmp.P,
		Alpha:        p.significance.Alpha,
		N:            cmp.N1,
		NHead:        cmp.N2,
		Better:       better,
		Warnings:     warningStrings(cmp.Warnings, baseSummary.Warnings, headSummary.Warnings),
	}
	// Identical samples on both sides are the one case where a warning from the
	// statistics means the opposite of uncertainty. The U-test refuses to run on
	// them ("all samples are equal") and benchmath reports P=1 with a warning,
	// which naively reads as unmeasurable — but "every measurement of both
	// revisions was the same number" is the most certain unchanged there is. It
	// is also the common case for allocs/op, where a well-behaved benchmark
	// reports the same integer every time, so misreading it fills a report with
	// spurious "unmeasurable" rows.
	identical := sameValues(base.values[k][m], head.values[k][m])
	switch {
	case identical:
		c.Verdict = VerdictUnchanged
		// Drop the statistics' warnings: they describe a test that declined to
		// run, and nothing here is uncertain.
		c.Warnings = nil
	case len(cmp.Warnings) > 0:
		// Too few samples to detect a difference at this alpha. Reporting that
		// as unchanged would claim knowledge the test explicitly disclaimed.
		c.Verdict = VerdictUnmeasurable
	default:
		c.Verdict = p.Verdict(c.DeltaPercent, better, cmp.P < cmp.Alpha)
	}
	if better == 0 && !identical {
		c.Warnings = append(c.Warnings,
			"no improvement direction known for unit "+c.Unit+
				"; emit a `"+c.Unit+" better=lower` (or higher) line to gate on it")
	}
	c.Warnings = append(c.Warnings, instabilityWarnings(&c)...)
	return c
}

// instabilityWarnings reports a measurement whose confidence interval is so
// wide that the verdict drawn from it should not be acted on.
//
// This is the failure mode a performance gate is most often wrong about: the
// statistics are applied correctly to measurements that were never stable
// enough to carry them, and the gate reports a confident verdict about noise.
// Saying so is the difference between a gate people trust and one they mute.
func instabilityWarnings(c *Comparison) []string {
	var out []string
	for _, side := range []struct {
		name           string
		center, lo, hi float64
		present        bool
	}{
		{"base", c.BaseCenter, c.BaseLo, c.BaseHi, c.hasBase()},
		{"head", c.HeadCenter, c.HeadLo, c.HeadHi, c.hasHead()},
	} {
		if !side.present {
			continue
		}
		r, ok := rangePercent(side.center, side.lo, side.hi)
		if !ok || r <= UnstableRangePercent {
			continue
		}
		out = append(out, fmt.Sprintf(
			"the %s measurement varied by ±%.0f%%, well over ±%.0f%%; "+
				"raise --rounds or --benchtime before trusting this verdict",
			side.name, r, UnstableRangePercent))
	}
	return out
}

// sameValues reports whether both samples consist entirely of one repeated
// number. A benchmark that allocates a fixed amount per operation produces
// exactly this, every round.
func sameValues(base, head []float64) bool {
	if len(base) == 0 || len(head) == 0 {
		return false
	}
	first := base[0]
	for _, v := range base {
		if v != first {
			return false
		}
	}
	for _, v := range head {
		if v != first {
			return false
		}
	}
	return true
}

// oneSided builds the rows for a benchmark that exists on only one side. There
// is nothing to test, so the single run's center is reported and the verdict
// carries the whole message.
func (s Series) oneSided(k Key, v Verdict, p Policy) []Comparison {
	th := benchmath.Thresholds{CompareAlpha: p.significance.Alpha}
	out := make([]Comparison, 0, len(s.values[k]))
	for _, m := range s.metrics(k) {
		if !p.gatesMetric(m) {
			continue
		}
		assumption := s.units.GetAssumption(string(m))
		summary := assumption.Summary(s.sample(k, m, &th), p.significance.Confidence)
		c := Comparison{
			Key:      k,
			Metric:   m,
			Unit:     s.displayUnit(m),
			Verdict:  v,
			Alpha:    p.significance.Alpha,
			Better:   s.units.GetBetter(string(m)),
			Warnings: warningStrings(summary.Warnings),
		}
		// Whichever side exists is the only one with a measurement; the absent
		// side stays at its zero value, and the verdict says which is which.
		if v == VerdictRemoved {
			c.BaseCenter, c.BaseLo, c.BaseHi, c.N = summary.Center, summary.Lo, summary.Hi, len(
				s.values[k][m],
			)
		} else {
			c.HeadCenter, c.HeadLo, c.HeadHi, c.NHead = summary.Center, summary.Lo, summary.Hi, len(
				s.values[k][m],
			)
		}
		out = append(out, c)
	}
	return out
}

// percentChange returns the change from old to new as a percentage.
//
// A zero baseline is real rather than exceptional: "0 allocs/op" is the goal
// for many benchmarks, and it is also the case where the ratio is undefined.
// Reporting 0% there would be the worst possible answer, because going from no
// allocations to some is the regression an allocation gate most needs to
// catch, and 0% clears every tolerance. So a move off a zero baseline returns
// an infinity, which exceeds any tolerance and carries the right sign. Zero to
// zero is genuinely no change.
//
// Callers must cope with a non-finite result: DeltaString renders it as ∞, and
// the JSON encoding omits the field, since JSON cannot represent an infinity.
func percentChange(old, updated float64) float64 {
	switch old {
	case updated:
		return 0
	case 0:
		return math.Inf(sign(updated))
	default:
		return (updated/old - 1) * 100
	}
}

// sign returns +1 for a positive number and -1 for a negative one.
func sign(f float64) int {
	if f < 0 {
		return -1
	}
	return 1
}

// warningStrings flattens several groups of warnings into messages, dropping
// repeats.
//
// The base and head summaries warn independently and, for the usual cause — too
// few samples for the requested confidence — word it identically. Printing the
// same sentence twice per row reads as two problems and buries the rows that
// have a different one.
func warningStrings(groups ...[]error) []string {
	var out []string
	for _, group := range groups {
		for _, err := range group {
			msg := err.Error()
			if !slices.Contains(out, msg) {
				out = append(out, msg)
			}
		}
	}
	return out
}
