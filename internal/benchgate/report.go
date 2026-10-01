package benchgate

// Exit codes. Distinct codes let a CI job tell "the tool broke" apart from
// "the tool worked and the code is slower", which are different problems with
// different owners.
const (
	// ExitPass means every gate the policy enabled was satisfied. Findings may
	// still have been reported.
	ExitPass = 0
	// ExitFail means the tool could not complete: git, the go command, or the
	// input. Reserved for main.go's error path, listed here so the full set is
	// documented in one place.
	ExitFail = 1
	// ExitRegression means a regression was found and Gates.Regression was on.
	ExitRegression = 2
	// ExitGap means a coverage gap was found and Gates.Gap was on.
	ExitGap = 3
)

// Revision names one side of a comparison.
type Revision struct {
	// Ref is what the operator asked for: "origin/main", "HEAD~1", or a path
	// for results read from a file.
	Ref string
	// SHA is the resolved commit, empty when the results came from a file
	// rather than a revision.
	SHA string
}

// Report is everything one gate run found. It is the single value the shell
// hands to a renderer, and the only thing that decides the exit code.
//
// Construct one with NewReport, which captures the policy the findings were
// judged against, so ExitCode needs no arguments and cannot be asked the
// question under a different policy than produced the findings.
//
// Its methods take a pointer receiver because a Report is a wide value and
// copying it per call is waste; none of them mutate it.
type Report struct {
	// Base and Head are the two revisions compared.
	Base, Head Revision
	// BenchCommand is the go command that produced the measurements, recorded
	// verbatim so a reader can reproduce the run.
	BenchCommand string
	// Platform and CPU describe the machine, for provenance.
	Platform, CPU string
	// Comparisons is every metric of every benchmark, in deterministic order.
	Comparisons []Comparison
	// Gaps is every benchmark-reach finding.
	Gaps []Gap
	// Coverage is the benchmark-only coverage profile, when one was gathered.
	Coverage CoverageProfile
	// Notices carries anything the operator should know that is neither a
	// regression nor a gap: a skipped run, or an arm that produced no results.
	Notices []string

	policy Policy
}

// Warning is one thing the statistics wanted to say about one comparison,
// labelled with which measurement it concerns.
type Warning struct {
	// Subject names the benchmark and unit, as the report's table labels it.
	Subject string
	// Detail is the message.
	Detail string
}

// Scope is what every comparison in a report has in common.
//
// A gate run almost always measures one package on one machine, so repeating
// the import path and the platform on every row costs most of the terminal
// width and tells the reader nothing new. Whatever is shared moves to the
// header; whatever varies stays on the row.
type Scope struct {
	// Pkg is the import path shared by every comparison, or "" when they differ.
	Pkg string
	// Platform is the goos/goarch shared by every comparison, or "" when they
	// differ.
	Platform string
}

// NewReport returns an empty Report whose findings will be judged against p.
func NewReport(p Policy) Report {
	return Report{
		Comparisons: make([]Comparison, 0),
		Gaps:        make([]Gap, 0),
		policy:      p,
	}
}

// ComparesRevisions reports whether this run compared two revisions at all.
//
// `benchgate gaps` produces a report with findings but no revisions, and a
// renderer that assumed otherwise would head it "benchmark gate: -> " and then
// announce that no benchmarks were compared — neither of which is a thing that
// happened.
func (r *Report) ComparesRevisions() bool {
	return r.Base.Ref != "" || r.Base.SHA != "" || r.Head.Ref != "" || r.Head.SHA != ""
}

// Policy returns the policy these findings were judged against.
func (r *Report) Policy() Policy { return r.policy }

// Regressions returns the comparisons that failed the gate.
func (r *Report) Regressions() []Comparison { return r.withVerdict(VerdictRegressed) }

// Improvements returns the comparisons that got significantly faster or
// smaller. They never affect the exit code; they are worth showing because a
// reviewer reading "no regressions" learns less than one reading what did move.
func (r *Report) Improvements() []Comparison { return r.withVerdict(VerdictImproved) }

// ExitCode returns the process exit code these findings imply under the
// report's policy.
//
// A regression outranks a gap: it is the more specific finding, and the one the
// author of the change can act on immediately.
//
// Ensures: ExitPass whenever the corresponding gate is off, whatever was found.
func (r *Report) ExitCode() int {
	if r.policy.gates.Regression && len(r.Regressions()) > 0 {
		return ExitRegression
	}
	if r.policy.gates.Gap && len(r.Gaps) > 0 {
		return ExitGap
	}
	return ExitPass
}

// Verdicts counts the comparisons by verdict, so a summary line can be written
// without each renderer re-deriving it.
func (r *Report) Verdicts() map[Verdict]int {
	counts := make(map[Verdict]int, len(r.Comparisons))
	for i := range r.Comparisons {
		counts[r.Comparisons[i].Verdict]++
	}
	return counts
}

// Warnings returns every warning attached to every comparison, in row order.
func (r *Report) Warnings() []Warning {
	scope := r.Scope()
	out := make([]Warning, 0)
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		for _, detail := range c.Warnings {
			out = append(out, Warning{
				Subject: scope.Label(c) + " " + c.Unit,
				Detail:  detail,
			})
		}
	}
	return out
}

// Scope returns what every comparison in this report has in common.
func (r *Report) Scope() Scope {
	if len(r.Comparisons) == 0 {
		return Scope{}
	}
	scope := Scope{
		Pkg:      r.Comparisons[0].Key.Pkg,
		Platform: r.Comparisons[0].Key.platform(),
	}
	for i := range r.Comparisons {
		if r.Comparisons[i].Key.Pkg != scope.Pkg {
			scope.Pkg = ""
		}
		if r.Comparisons[i].Key.platform() != scope.Platform {
			scope.Platform = ""
		}
	}
	return scope
}

// Label returns the row label for a comparison, leaving out whatever the
// report's header already states.
func (s Scope) Label(c *Comparison) string {
	switch {
	case s.Pkg != "" && s.Platform != "":
		return c.Key.Name
	case s.Pkg != "":
		return c.Key.Name + " [" + c.Key.platform() + "]"
	case s.Platform != "":
		return c.Key.qualifiedName()
	default:
		return c.Key.String()
	}
}

func (r *Report) withVerdict(v Verdict) []Comparison {
	out := make([]Comparison, 0)
	for i := range r.Comparisons {
		if r.Comparisons[i].Verdict == v {
			out = append(out, r.Comparisons[i])
		}
	}
	return out
}
