// Package gotest runs the Go toolchain: benchmarks, benchmark listings and
// coverage reports.
//
// It is the adapter for one external dependency — the `go` command — and it is
// deliberately thin. It builds argument lists, runs processes and returns their
// bytes. Deciding what the bytes mean belongs to internal/benchgate, which can
// do it without a subprocess (summary_rules §5).
package gotest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Defaults for the measurement options. Exported so the flag definitions and
// the documentation share one source of truth.
const (
	// DefaultPackages is the package pattern to benchmark.
	DefaultPackages = "./..."
	// DefaultBench is the benchmark name pattern. "." matches every benchmark.
	DefaultBench = "."
	// DefaultCount is the -count passed to each individual run. The gate's own
	// --rounds is what accumulates samples, because interleaving the two arms
	// is what makes the samples comparable; raising -count instead collects
	// every base sample before any head sample.
	DefaultCount = 1
	// DefaultBenchtime is the per-benchmark time budget. Empty means the go
	// default of 1s.
	DefaultBenchtime = ""
)

// Options is how to invoke `go test -bench`. Its methods take a pointer
// receiver because it is a wide value and none of them mutate it.
//
// There is deliberately no raw argument-string or template escape hatch. The
// fields below cover what `go test -bench` offers that changes a measurement,
// and a second way to say the same thing is a place for the two to disagree
// (summary_rules §4). A project whose measurements genuinely cannot be
// expressed here can produce result files however it likes and compare them
// with `benchgate compare`.
type Options struct {
	// Packages is the pattern to benchmark, e.g. "./..." or "./internal/hot".
	Packages string
	// Bench is the -bench regexp selecting which benchmarks to run.
	Bench string
	// Count is the -count for one run.
	Count int
	// Benchtime is the -benchtime value, e.g. "100x" or "2s". Empty uses the
	// go default.
	Benchtime string
	// CPU is the -cpu list, e.g. "1,4". Empty uses the current GOMAXPROCS.
	CPU string
	// Tags is the -tags value. Empty passes no build tags.
	Tags string
	// Benchmem adds -benchmem, so B/op and allocs/op are measured. Allocation
	// counts are far less noisy than wall time on a shared runner, which makes
	// them the more trustworthy half of a CI gate.
	Benchmem bool
}

// Runner invokes the go toolchain in a directory.
//
// The zero Runner uses "go" from PATH in the process's working directory and
// discards diagnostic output, so it is usable as-is.
type Runner struct {
	// GoCmd is the go binary to run. Empty means "go", resolved on PATH.
	GoCmd string
	// Dir is the working directory for every command. Empty means the
	// process's own.
	Dir string
	// Debug receives each command line and the stderr of each command. Nil
	// discards both.
	Debug io.Writer
}

// Args returns the `go test` arguments these options describe, after the
// program name.
//
// -run '^$' is what makes this a benchmark-only run: a regexp matching no test
// name, so unit tests neither consume time nor contribute to the coverage
// profile the gate reads.
func (o *Options) Args() []string {
	args := []string{"test", o.packages(), "-run", "^$", "-bench", o.bench()}
	if c := o.count(); c > 0 {
		args = append(args, "-count", strconv.Itoa(c))
	}
	if o.Benchtime != "" {
		args = append(args, "-benchtime", o.Benchtime)
	}
	if o.CPU != "" {
		args = append(args, "-cpu", o.CPU)
	}
	if o.Tags != "" {
		args = append(args, "-tags", o.Tags)
	}
	if o.Benchmem {
		args = append(args, "-benchmem")
	}
	return args
}

// CoverArgs returns the arguments for an instrumented benchmark run writing its
// profile to the given path.
//
// -coverpkg is not optional here. Without it a benchmark instruments only its
// own package, so every statement a benchmark reaches through a call into
// another package reads as uncovered and any coverage floor is unmeetable —
// the defect in the reference implementation this tool replaces.
func (o *Options) CoverArgs(profilePath string) []string {
	return append(o.Args(),
		"-covermode", "atomic",
		"-coverpkg", o.packages(),
		"-coverprofile", profilePath,
	)
}

// String renders the command line these options produce, for the report. It is
// the command a reader can paste into a shell to reproduce the measurement.
func (o *Options) String() string {
	return "go " + strings.Join(o.Args(), " ")
}

// Bench runs the benchmarks and returns their output verbatim.
//
// A non-zero exit is an error even when some output was produced: a package
// that failed to build contributes no measurements, and silently comparing the
// remainder would report a narrower gate than the operator asked for.
func (r Runner) Bench(ctx context.Context, opts *Options) ([]byte, error) {
	out, err := r.run(ctx, opts.Args()...)
	if err != nil {
		return nil, fmt.Errorf("running benchmarks: %w", err)
	}
	return out, nil
}

// BenchWithCover runs the benchmarks with coverage instrumentation, writing the
// profile to profilePath, and returns the benchmark output.
//
// The measurements from this run are instrumented and therefore slower than the
// real thing; they are read for coverage only. The gate compares the
// uninstrumented runs.
func (r Runner) BenchWithCover(
	ctx context.Context,
	opts *Options,
	profilePath string,
) ([]byte, error) {
	out, err := r.run(ctx, opts.CoverArgs(profilePath)...)
	if err != nil {
		return nil, fmt.Errorf("running instrumented benchmarks: %w", err)
	}
	return out, nil
}

// ListBenchmarks returns the output of `go test -list '^Benchmark'`, which names
// every benchmark function in every matching package plus a status line per
// package. Nothing is executed.
func (r Runner) ListBenchmarks(ctx context.Context, packages string) ([]byte, error) {
	if packages == "" {
		packages = DefaultPackages
	}
	out, err := r.run(ctx, "test", "-list", "^Benchmark", packages)
	if err != nil {
		return nil, fmt.Errorf("listing benchmarks: %w", err)
	}
	return out, nil
}

// CoverFunc returns the output of `go tool cover -func` for a profile.
//
// `go tool cover` ships with the toolchain, so reading the profile through it
// adds no dependency and no version skew: the tool that wrote the profile is
// the tool that reads it.
func (r Runner) CoverFunc(ctx context.Context, profilePath string) ([]byte, error) {
	out, err := r.run(ctx, "tool", "cover", "-func="+profilePath)
	if err != nil {
		return nil, fmt.Errorf("reading coverage profile: %w", err)
	}
	return out, nil
}

func (o *Options) packages() string {
	if o.Packages == "" {
		return DefaultPackages
	}
	return o.Packages
}

func (o *Options) bench() string {
	if o.Bench == "" {
		return DefaultBench
	}
	return o.Bench
}

func (o *Options) count() int {
	if o.Count <= 0 {
		return DefaultCount
	}
	return o.Count
}

// run executes one go command and returns its standard output.
//
// Standard error is captured separately and folded into the error rather than
// into the result, because the result is parsed as benchmark output and a
// compiler diagnostic in the middle of it would read as a malformed record.
func (r Runner) run(ctx context.Context, args ...string) ([]byte, error) {
	// The command name comes from a flag so a project can point at a toolchain
	// wrapper, and the arguments from flags so a project can choose what to
	// measure. Both are the operator's own input, in the operator's own
	// checkout — the same trust level as the `go test` line they would type.
	cmd := exec.CommandContext(ctx, r.goCmd(), args...) //nolint:gosec // G204, see above
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	r.debugf("+ %s %s\n", r.goCmd(), strings.Join(args, " "))

	err := cmd.Run()
	if stderr.Len() > 0 {
		r.debugf("%s", stderr.String())
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s",
			r.goCmd(), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (r Runner) goCmd() string {
	if r.GoCmd == "" {
		return "go"
	}
	return r.GoCmd
}

// debugf writes a diagnostic line. A failed write to a diagnostic stream is
// not worth reacting to: there is nowhere better to report it, and failing the
// gate because the verbose log could not be written would be absurd.
func (r Runner) debugf(format string, args ...any) {
	if r.Debug == nil {
		return
	}
	_, _ = fmt.Fprintf(r.Debug, format, args...)
}
