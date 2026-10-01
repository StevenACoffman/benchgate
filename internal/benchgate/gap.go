package benchgate

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

// The possible GapKind values.
const (
	// GapNoBenchmark means a package declares no Benchmark function at all.
	// Whatever it does, no measurement will ever notice it getting slower.
	GapNoBenchmark GapKind = "no-benchmark"
	// GapUnreached means a function exists but the benchmark run never
	// executed a single one of its statements. A benchmark suite that misses
	// the branch a change touches cannot detect that change's cost.
	GapUnreached GapKind = "unreached"
	// GapBelowFloor means aggregate benchmark-only statement coverage fell
	// under the configured minimum. This is the one aggregate gap; the other
	// two name specific code.
	GapBelowFloor GapKind = "below-floor"
)

// GapKind names a way in which benchmarks fail to speak for the code.
type GapKind string

// Gap is one finding about benchmark reach.
type Gap struct {
	// Kind classifies the finding.
	Kind GapKind
	// Package is the import path the finding concerns, empty for
	// GapBelowFloor, which is about the whole module.
	Package string
	// Function is the function name, set only for GapUnreached.
	Function string
	// Position is "file.go:line" for GapUnreached, so an editor or a CI
	// annotation can jump straight to it.
	Position string
	// Detail is a sentence a human can act on.
	Detail string
}

// FuncCoverage is one row of `go tool cover -func` output.
type FuncCoverage struct {
	// Position is the "import/path/file.go:line" the row names.
	Position string
	// Package is the import path Position sits in.
	Package string
	// Function is the function name.
	Function string
	// Percent is the share of the function's statements that executed.
	Percent float64
}

// CoverageProfile is a parsed `go tool cover -func` report.
type CoverageProfile struct {
	// Funcs is every function row, in the order reported.
	Funcs []FuncCoverage
	// TotalPercent is the aggregate from the trailing "total:" row.
	TotalPercent float64
	// HasTotal distinguishes a genuine 0% total from a report that carried no
	// total row at all, which means the profile was empty.
	HasTotal bool
}

// BenchmarkIndex maps every package in the module to the benchmark functions it
// declares. A package with no benchmarks is present with an empty slice, which
// is what makes GapNoBenchmark detectable.
type BenchmarkIndex map[string][]string

// ParseFuncCoverage reads `go tool cover -func` output.
//
// The columns are position, function name and percentage, separated by runs of
// tabs that tabwriter has padded. Fields are read from the right — percentage
// last, name next — rather than from the left, so a file path containing a
// space cannot shift the columns.
//
// Ensures: rows appear in the order read; an empty input yields a zero
// CoverageProfile with HasTotal false and no error.
func ParseFuncCoverage(r io.Reader) (CoverageProfile, error) {
	var prof CoverageProfile
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pct, err := parsePercent(fields[len(fields)-1])
		if err != nil {
			continue // a header, a warning, or anything else go printed
		}
		if strings.HasPrefix(line, "total:") {
			prof.TotalPercent, prof.HasTotal = pct, true
			continue
		}
		pos := strings.Join(fields[:len(fields)-2], " ")
		prof.Funcs = append(prof.Funcs, FuncCoverage{
			Position: strings.TrimSuffix(pos, ":"),
			Package:  packageOfPosition(pos),
			Function: fields[len(fields)-2],
			Percent:  pct,
		})
	}
	if err := scanner.Err(); err != nil {
		return CoverageProfile{}, fmt.Errorf("reading coverage report: %w", err)
	}
	return prof, nil
}

// ParseBenchmarkList reads `go test -list <regexp> ./...` output.
//
// That output is a flat stream: the matching function names for a package,
// then a terminator naming it — "ok <pkg> 0.2s", "? <pkg> [no test files]" or
// "FAIL <pkg> ...". Names are therefore accumulated and attributed when the
// terminator arrives. Running one `go test -list` per package would avoid the
// parsing, at the cost of one process per package.
//
// Ensures: every package the command reported is a key, including packages with
// no benchmarks, which map to an empty slice.
func ParseBenchmarkList(r io.Reader) (BenchmarkIndex, error) {
	index := make(BenchmarkIndex)
	var pending []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		pkg, isTerminator := packageTerminator(line)
		if !isTerminator {
			if name := strings.TrimSpace(line); strings.HasPrefix(name, "Benchmark") {
				pending = append(pending, name)
			}
			continue
		}
		index[pkg] = pending
		pending = nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading benchmark list: %w", err)
	}
	return index, nil
}

// FindGaps classifies everything p asks to be told about.
//
// Findings are ordered kind by kind — the aggregate floor first, then missing
// benchmarks by package, then unreached functions in report order — so two runs
// over the same input produce byte-identical output.
//
// Requires: p was returned by NewPolicy.
// Ensures:  the result is empty when p asks for no gap checks, whatever prof
// and index contain.
func FindGaps(prof CoverageProfile, index BenchmarkIndex, p Policy) []Gap {
	gaps := make([]Gap, 0)
	gaps = append(gaps, belowFloorGap(prof, p.coverage)...)
	if p.coverage.RequirePerPackage {
		gaps = append(gaps, noBenchmarkGaps(index)...)
	}
	if p.coverage.ReportUnreached {
		gaps = append(gaps, unreachedGaps(prof)...)
	}
	return gaps
}

func belowFloorGap(prof CoverageProfile, cov Coverage) []Gap {
	if cov.MinPercent <= 0 || !prof.HasTotal || prof.TotalPercent >= cov.MinPercent {
		return nil
	}
	return []Gap{{
		Kind: GapBelowFloor,
		Detail: fmt.Sprintf(
			"benchmarks execute %.1f%% of statements, below the %.1f%% floor",
			prof.TotalPercent, cov.MinPercent),
	}}
}

func noBenchmarkGaps(index BenchmarkIndex) []Gap {
	bare := make([]string, 0, len(index))
	for pkg, benchmarks := range index {
		if len(benchmarks) == 0 {
			bare = append(bare, pkg)
		}
	}
	slices.Sort(bare) // map iteration order is randomised; output must not be
	gaps := make([]Gap, 0, len(bare))
	for _, pkg := range bare {
		gaps = append(gaps, Gap{
			Kind:    GapNoBenchmark,
			Package: pkg,
			Detail:  "package declares no Benchmark function",
		})
	}
	return gaps
}

func unreachedGaps(prof CoverageProfile) []Gap {
	gaps := make([]Gap, 0)
	for _, fn := range prof.Funcs {
		if fn.Percent > 0 {
			continue
		}
		gaps = append(gaps, Gap{
			Kind:     GapUnreached,
			Package:  fn.Package,
			Function: fn.Function,
			Position: fn.Position,
			Detail:   "no benchmark executes any statement in this function",
		})
	}
	return gaps
}

// packageTerminator recognises the per-package status line `go test` prints
// after each package's listing, and returns the import path it names.
func packageTerminator(line string) (pkg string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false
	}
	switch fields[0] {
	case "ok", "?", "FAIL", "---":
		return fields[1], true
	default:
		return "", false
	}
}

// packageOfPosition turns "import/path/file.go:12:" into "import/path".
// Coverage positions are always slash-separated regardless of host OS, so this
// uses path rather than filepath.
func packageOfPosition(pos string) string {
	file, _, _ := strings.Cut(pos, ":")
	return path.Dir(file)
}

func parsePercent(field string) (float64, error) {
	trimmed, ok := strings.CutSuffix(field, "%")
	if !ok {
		return 0, Errorf(EINVALID, "not a percentage: %q", field)
	}
	pct, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, Errorf(EINVALID, "not a percentage: %q", field)
	}
	return pct, nil
}

// String renders a Gap as a single reviewable line.
func (g Gap) String() string {
	switch g.Kind {
	case GapBelowFloor:
		return g.Detail
	case GapNoBenchmark:
		return g.Package + ": " + g.Detail
	case GapUnreached:
		return g.Position + ": " + g.Function + ": " + g.Detail
	default:
		return string(g.Kind) + ": " + g.Detail
	}
}
