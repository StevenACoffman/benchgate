package benchgate

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
	"golang.org/x/perf/benchunit"
)

// Metric is a tidied benchmark unit — "sec/op", "B/op", "allocs/op", "B/s".
//
// It is a distinct type because the reader tidies units on the way in ("ns/op"
// becomes "sec/op", "MB/s" becomes "B/s") while operators and benchmark output
// use the untidied spelling. Keying a map or a direction lookup on an untidied
// string silently finds nothing, so the conversion happens once, in ParseMetric,
// and the type makes it impossible to skip.
type Metric string

// Key identifies one benchmark for pairing across two revisions.
//
// The machine fields are part of the identity, not decoration. A darwin/arm64
// measurement and a linux/amd64 measurement of the same function are not two
// samples of one thing, and pairing them would produce a confident p-value for
// a meaningless question. Including them here means mismatched pairs never pair
// up at all.
type Key struct {
	Pkg    string
	Name   string
	GOOS   string
	GOARCH string
}

// Series is every measurement read from one benchmark run, grouped by benchmark
// and unit.
//
// The zero Series is empty and safe to use. Construct a populated one with
// ParseResults.
type Series struct {
	// values maps a benchmark and unit to every value observed for it, in the
	// order read.
	values map[Key]map[Metric][]float64
	// origUnits records the untidied spelling each tidied unit came from, so a
	// report can say "ns/op" where that is what the benchmark printed.
	origUnits map[Metric]string
	// keys preserves first-appearance order so output is deterministic without
	// imposing an alphabetical order nobody asked for.
	keys []Key
	// units carries the unit metadata the reader accumulated: the improvement
	// direction and distributional assumption per unit, including any
	// `better=` or `assume=` lines the benchmark emitted.
	units benchfmt.UnitMetadataMap
	// cpu is the "cpu:" configuration line, kept for the report header only.
	cpu string
}

// ParseResults reads `go test -bench` output and groups every measurement by
// benchmark and unit.
//
// name labels the stream in syntax-error messages. Malformed lines are skipped
// rather than fatal: `go test` interleaves build output, PASS lines and
// benchmark records on one stream, and a run that produced usable records
// should not be discarded because it also printed something else. A stream
// containing no records at all yields an empty Series and no error — whether
// that is a problem is the caller's policy, not this function's.
//
// Ensures: the returned Series shares no memory with r's contents; calling it
// twice on equal input yields equal Series.
func ParseResults(r io.Reader, name string) (Series, error) {
	s := Series{
		values:    make(map[Key]map[Metric][]float64),
		origUnits: make(map[Metric]string),
	}
	reader := benchfmt.NewReader(r, name)
	for reader.Scan() {
		// benchfmt reuses one *Result across iterations — "Results are
		// designed to be mutated in place and reused to reduce allocation".
		// Everything retained below is therefore copied out of it; holding the
		// pointer, its Values slice or its Config slice would leave every
		// entry showing the last record read.
		res, ok := reader.Result().(*benchfmt.Result)
		if !ok {
			continue // a SyntaxError or a unit-metadata record
		}
		s.add(res)
	}
	if err := reader.Err(); err != nil {
		return Series{}, fmt.Errorf("reading benchmark results from %s: %w", name, err)
	}
	s.units = reader.Units()
	return s, nil
}

// ParseMetric tidies a unit as a human writes it into the form the benchmark
// reader produces, so "ns/op" and "sec/op" name the same metric.
func ParseMetric(unit string) (Metric, error) {
	trimmed := strings.TrimSpace(unit)
	if trimmed == "" {
		return "", Errorf(EINVALID, "metric must not be empty")
	}
	_, tidy := benchunit.Tidy(1, trimmed)
	return Metric(tidy), nil
}

// Keys returns the benchmarks in this Series, in the order they were first
// read.
func (s Series) Keys() []Key { return slices.Clone(s.keys) }

// Len returns the number of distinct benchmarks in this Series.
func (s Series) Len() int { return len(s.keys) }

// CPU returns the "cpu:" line from the benchmark output, or "" if there was
// none. It is reported to the reader as provenance and never compared.
func (s Series) CPU() string { return s.cpu }

// Platform returns the goos/goarch the results were measured on, or "" when the
// Series is empty. Results from more than one platform in a single stream are
// reported as "mixed": that is not a thing the gate can compare, and saying so
// is more useful than naming whichever one happened to be read first.
func (s Series) Platform() string {
	var seen string
	for _, k := range s.keys {
		p := k.GOOS + "/" + k.GOARCH
		switch {
		case seen == "":
			seen = p
		case seen != p:
			return "mixed"
		}
	}
	return seen
}

// metrics returns the units recorded for one benchmark, in read order.
func (s Series) metrics(k Key) []Metric {
	// The order `go test -benchmem` prints units in. Report rows follow it so a
	// reader sees the familiar sequence rather than Go's randomised map order.
	conventional := []Metric{"sec/op", "B/op", "allocs/op", "B/s"}
	byMetric := s.values[k]
	out := make([]Metric, 0, len(byMetric))
	for _, m := range conventional {
		if _, ok := byMetric[m]; ok {
			out = append(out, m)
		}
	}
	// Anything outside the conventional order — a custom unit — follows, sorted
	// so the output is stable across runs.
	extra := make([]Metric, 0, len(byMetric))
	for m := range byMetric {
		if !slices.Contains(out, m) {
			extra = append(extra, m)
		}
	}
	slices.Sort(extra)
	return append(out, extra...)
}

// sample builds a statistical sample for one benchmark and unit.
//
// The values are cloned because benchmath.NewSample sorts the slice it is
// given, in place. Handing it the Series' own slice would reorder data the
// caller still owns and make a second call on the same Series observe a
// different ordering (summary_rules §5).
func (s Series) sample(k Key, m Metric, th *benchmath.Thresholds) *benchmath.Sample {
	return benchmath.NewSample(slices.Clone(s.values[k][m]), th)
}

func (s *Series) add(res *benchfmt.Result) {
	key := Key{
		Pkg: res.GetConfig("pkg"),
		// benchfmt strips the "Benchmark" prefix while parsing. It is restored
		// here because the full name is the actionable one: it is what a reader
		// pastes into `go test -bench` to reproduce the row.
		Name:   "Benchmark" + res.Name.String(),
		GOOS:   res.GetConfig("goos"),
		GOARCH: res.GetConfig("goarch"),
	}
	if s.cpu == "" {
		s.cpu = res.GetConfig("cpu")
	}
	byMetric, ok := s.values[key]
	if !ok {
		byMetric = make(map[Metric][]float64, len(res.Values))
		s.values[key] = byMetric
		s.keys = append(s.keys, key)
	}
	for _, v := range res.Values {
		m := Metric(v.Unit)
		byMetric[m] = append(byMetric[m], v.Value)
		if _, seen := s.origUnits[m]; !seen && v.OrigUnit != "" {
			s.origUnits[m] = v.OrigUnit
		}
	}
}

// displayUnit returns the spelling to show a reader: the untidied unit the
// benchmark printed when one was recorded, else the tidied unit itself.
func (s Series) displayUnit(m Metric) string {
	if orig, ok := s.origUnits[m]; ok {
		return orig
	}
	return string(m)
}

// String renders a Key as "pkg.Benchmark/sub [goos/goarch]", omitting whichever
// parts the benchmark output did not carry.
func (k Key) String() string {
	var b strings.Builder
	if k.Pkg != "" {
		b.WriteString(k.Pkg)
		b.WriteString(".")
	}
	b.WriteString(k.Name)
	if k.GOOS != "" || k.GOARCH != "" {
		fmt.Fprintf(&b, " [%s/%s]", k.GOOS, k.GOARCH)
	}
	return b.String()
}

// platform renders the machine half of a Key as "goos/goarch", or "" when the
// benchmark output carried neither.
func (k Key) platform() string {
	if k.GOOS == "" && k.GOARCH == "" {
		return ""
	}
	return k.GOOS + "/" + k.GOARCH
}

// qualifiedName renders a Key as "pkg.Benchmark/sub", without the platform.
func (k Key) qualifiedName() string {
	if k.Pkg == "" {
		return k.Name
	}
	return k.Pkg + "." + k.Name
}
