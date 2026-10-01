package benchgate

import (
	"fmt"
	"math"
	"strconv"

	"golang.org/x/perf/benchunit"
)

// The string every formatter uses for a value that does not exist, as opposed
// to a value that is zero. A benchmark present on only one side genuinely has
// no number on the other, and printing "0" there would read as a measurement.
const absent = "-"

// The helpers below take a pointer receiver because Comparison is a wide value
// (eight floats, four strings and a slice) and gocritic rightly objects to
// copying it per call. None of them mutate the receiver.

// hasBase reports whether this comparison has a measurement at the base
// revision. VerdictAdded is the one case with none.
func (c *Comparison) hasBase() bool { return c.Verdict != VerdictAdded }

// hasHead reports whether this comparison has a measurement at the head
// revision. VerdictRemoved is the one case with none.
func (c *Comparison) hasHead() bool { return c.Verdict != VerdictRemoved }

// DeltaString renders the change as a signed percentage, or "-" when there is
// nothing to compare against.
//
// Note that the sign is the direction the *number* moved, not the direction the
// code moved: for a B/s metric, "+30%" is good news. The verdict column is what
// states the judgement.
func (c *Comparison) DeltaString() string {
	if !c.hasBase() || !c.hasHead() {
		return absent
	}
	// A move off a zero baseline has no finite percentage. "+∞%" says what
	// happened; "+Inf%", which is what %f would print, reads like a bug.
	if math.IsInf(c.DeltaPercent, 1) {
		return "+∞%"
	}
	if math.IsInf(c.DeltaPercent, -1) {
		return "-∞%"
	}
	return fmt.Sprintf("%+.2f%%", c.DeltaPercent)
}

// PString renders the p-value, or "-" when no test was run.
//
// Three decimals is the reporting convention, but it turns every p below 0.0005
// into "0.000", which reads as an exact result when it is merely a very small
// one. Those are rendered "<0.001" instead, leaving "0" to mean what benchmath
// means by P == 0: an exact result, where the two distributions do not overlap
// at all.
func (c *Comparison) PString() string {
	if !c.hasBase() || !c.hasHead() {
		return absent
	}
	const smallest = 0.001
	switch {
	case c.P == 0:
		return "0"
	case c.P < smallest:
		return "<0.001"
	default:
		return strconv.FormatFloat(c.P, 'f', 3, 64)
	}
}

// NString renders the sample counts: "10" when both sides have the same number
// of samples, "8+10" when they differ.
func (c *Comparison) NString() string {
	switch {
	case !c.hasBase():
		return strconv.Itoa(c.NHead)
	case !c.hasHead():
		return strconv.Itoa(c.N)
	case c.N == c.NHead:
		return strconv.Itoa(c.N)
	default:
		return strconv.Itoa(c.N) + "+" + strconv.Itoa(c.NHead)
	}
}

// BaseCell renders the base revision's center estimate with its confidence
// interval, in the "120.0n ± 2%" form benchstat uses. The range is what tells a
// reader whether the sample was large and stable enough for the verdict to mean
// anything, so it belongs next to the number rather than in a separate column.
func (c *Comparison) BaseCell() string {
	return cell(c.BaseCenter, c.BaseLo, c.BaseHi, c.Metric, c.hasBase())
}

// HeadCell renders the head revision's center estimate and confidence
// interval, in the same form as BaseCell.
func (c *Comparison) HeadCell() string {
	return cell(c.HeadCenter, c.HeadLo, c.HeadHi, c.Metric, c.hasHead())
}

func cell(center, lo, hi float64, m Metric, present bool) string {
	if !present {
		return absent
	}
	scaled := benchunit.Scale(center, benchunit.ClassOf(string(m)))
	r, ok := rangePercent(center, lo, hi)
	if !ok {
		return scaled
	}
	return fmt.Sprintf("%s ± %.0f%%", scaled, r)
}

// rangePercent expresses a confidence interval as a percentage of the center,
// reporting !ok when that cannot be done meaningfully: an unbounded interval
// (too few samples), an interval straddling zero, or a zero center.
func rangePercent(center, lo, hi float64) (pct float64, ok bool) {
	if math.IsInf(lo, 0) || math.IsInf(hi, 0) || center == 0 {
		return 0, false
	}
	if (center > 0) != (lo > 0) || (center > 0) != (hi > 0) {
		return 0, false
	}
	return 100 * max(hi/center-1, 1-lo/center), true
}
