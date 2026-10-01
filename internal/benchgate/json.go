package benchgate

// The types in this file are the JSON contract. They are separate from the
// domain types on purpose: a field rename in Comparison is a refactor, whereas a
// field rename here breaks every consumer, and the two should not be the same
// edit. Every field carries an explicit tag so the wire names cannot drift with
// a Go identifier.

type reportJSON struct {
	Base         revisionJSON     `json:"base"`
	Head         revisionJSON     `json:"head"`
	BenchCommand string           `json:"bench_command,omitempty"`
	Platform     string           `json:"platform,omitempty"`
	CPU          string           `json:"cpu,omitempty"`
	Policy       policyJSON       `json:"policy"`
	Comparisons  []comparisonJSON `json:"comparisons"`
	Gaps         []gapJSON        `json:"gaps"`
	Coverage     *coverageJSON    `json:"coverage,omitempty"`
	Notices      []string         `json:"notices,omitempty"`
	Counts       map[string]int   `json:"counts"`
	Regressions  int              `json:"regressions"`
	ExitCode     int              `json:"exit_code"`
}

type revisionJSON struct {
	Ref string `json:"ref,omitempty"`
	SHA string `json:"sha,omitempty"`
}

type policyJSON struct {
	Alpha              float64  `json:"alpha"`
	TolerancePercent   float64  `json:"tolerance_percent"`
	Confidence         float64  `json:"confidence"`
	MinCoveragePercent float64  `json:"min_coverage_percent"`
	Metrics            []string `json:"metrics,omitempty"`
	FailOnRegression   bool     `json:"fail_on_regression"`
	FailOnGap          bool     `json:"fail_on_gap"`
}

type comparisonJSON struct {
	Package      string   `json:"package,omitempty"`
	Name         string   `json:"name"`
	GOOS         string   `json:"goos,omitempty"`
	GOARCH       string   `json:"goarch,omitempty"`
	Metric       string   `json:"metric"`
	Unit         string   `json:"unit"`
	Verdict      string   `json:"verdict"`
	BaseCenter   *float64 `json:"base_center,omitempty"`
	HeadCenter   *float64 `json:"head_center,omitempty"`
	BaseRangePct *float64 `json:"base_range_percent,omitempty"`
	HeadRangePct *float64 `json:"head_range_percent,omitempty"`
	DeltaPercent *float64 `json:"delta_percent,omitempty"`
	P            *float64 `json:"p,omitempty"`
	Alpha        float64  `json:"alpha"`
	SamplesBase  int      `json:"samples_base"`
	SamplesHead  int      `json:"samples_head"`
	Better       int      `json:"better"`
	Warnings     []string `json:"warnings,omitempty"`
}

type gapJSON struct {
	Kind     string `json:"kind"`
	Package  string `json:"package,omitempty"`
	Function string `json:"function,omitempty"`
	Position string `json:"position,omitempty"`
	Detail   string `json:"detail"`
}

type coverageJSON struct {
	TotalPercent     float64 `json:"total_percent"`
	Functions        int     `json:"functions"`
	UnreachedFuncs   int     `json:"unreached_functions"`
	FloorPercent     float64 `json:"floor_percent"`
	MeetsFloorOrNone bool    `json:"meets_floor"`
}

func newReportJSON(r *Report) reportJSON {
	out := reportJSON{
		Base:         revisionJSON{Ref: r.Base.Ref, SHA: r.Base.SHA},
		Head:         revisionJSON{Ref: r.Head.Ref, SHA: r.Head.SHA},
		BenchCommand: r.BenchCommand,
		Platform:     r.Platform,
		CPU:          r.CPU,
		Policy:       newPolicyJSON(r.policy),
		Comparisons:  make([]comparisonJSON, 0, len(r.Comparisons)),
		Gaps:         make([]gapJSON, 0, len(r.Gaps)),
		Coverage:     newCoverageJSON(r.Coverage, r.policy),
		Notices:      r.Notices,
		Counts:       make(map[string]int, len(r.Comparisons)),
		Regressions:  len(r.Regressions()),
		ExitCode:     r.ExitCode(),
	}
	for verdict, n := range r.Verdicts() {
		out.Counts[string(verdict)] = n
	}
	for i := range r.Comparisons {
		out.Comparisons = append(out.Comparisons, newComparisonJSON(&r.Comparisons[i]))
	}
	for i := range r.Gaps {
		g := &r.Gaps[i]
		out.Gaps = append(out.Gaps, gapJSON{
			Kind:     string(g.Kind),
			Package:  g.Package,
			Function: g.Function,
			Position: g.Position,
			Detail:   g.Detail,
		})
	}
	return out
}

func newPolicyJSON(p Policy) policyJSON {
	metrics := make([]string, 0, len(p.metrics))
	for _, m := range p.metrics {
		metrics = append(metrics, string(m))
	}
	return policyJSON{
		Alpha:              p.significance.Alpha,
		TolerancePercent:   p.significance.Tolerance,
		Confidence:         p.significance.Confidence,
		MinCoveragePercent: p.coverage.MinPercent,
		Metrics:            metrics,
		FailOnRegression:   p.gates.Regression,
		FailOnGap:          p.gates.Gap,
	}
}

func newComparisonJSON(c *Comparison) comparisonJSON {
	out := comparisonJSON{
		Package:     c.Key.Pkg,
		Name:        c.Key.Name,
		GOOS:        c.Key.GOOS,
		GOARCH:      c.Key.GOARCH,
		Metric:      string(c.Metric),
		Unit:        c.Unit,
		Verdict:     string(c.Verdict),
		Alpha:       c.Alpha,
		SamplesBase: c.N,
		SamplesHead: c.NHead,
		Better:      c.Better,
		Warnings:    c.Warnings,
	}
	// A one-sided comparison has no base or head number, and no delta or
	// p-value at all. Pointers keep those fields absent from the JSON rather
	// than present as a zero a consumer would read as a measurement. Each
	// addresses a local copy rather than a field of c, so the DTO does not alias
	// the Comparison it was built from.
	if c.hasBase() {
		base := c.BaseCenter
		out.BaseCenter = &base
		if r, ok := rangePercent(c.BaseCenter, c.BaseLo, c.BaseHi); ok {
			out.BaseRangePct = &r
		}
	}
	if c.hasHead() {
		head := c.HeadCenter
		out.HeadCenter = &head
		if r, ok := rangePercent(c.HeadCenter, c.HeadLo, c.HeadHi); ok {
			out.HeadRangePct = &r
		}
	}
	if c.hasBase() && c.hasHead() {
		delta, p := c.DeltaPercent, c.P
		out.DeltaPercent, out.P = &delta, &p
	}
	return out
}

func newCoverageJSON(prof CoverageProfile, p Policy) *coverageJSON {
	if !prof.HasTotal {
		return nil
	}
	unreached := 0
	for _, fn := range prof.Funcs {
		if fn.Percent == 0 {
			unreached++
		}
	}
	return &coverageJSON{
		TotalPercent:     prof.TotalPercent,
		Functions:        len(prof.Funcs),
		UnreachedFuncs:   unreached,
		FloorPercent:     p.coverage.MinPercent,
		MeetsFloorOrNone: p.coverage.MinPercent <= 0 || prof.TotalPercent >= p.coverage.MinPercent,
	}
}
