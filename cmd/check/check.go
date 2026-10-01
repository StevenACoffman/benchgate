// Package check implements the "check" CLI command: the full CI gate.
package check

import (
	"context"
	"fmt"
	"time"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/benchgate/cmd/root"
	"github.com/StevenACoffman/benchgate/internal/benchgate"
	"github.com/StevenACoffman/benchgate/internal/gate"
	"github.com/StevenACoffman/benchgate/internal/gitwork"
	"github.com/StevenACoffman/benchgate/internal/gotest"
)

// Config holds the configuration for the check command.
type Config struct {
	*root.Config

	// measurement
	base       string
	packages   string
	bench      string
	count      int
	benchtime  string
	cpu        string
	tags       string
	benchmem   bool
	rounds     int
	interleave bool
	warmup     time.Duration
	cooldown   time.Duration

	// decision rule
	alpha      float64
	tolerance  float64
	confidence float64
	metrics    []string

	// benchmark coverage
	minCoverage       float64
	requirePerPackage bool
	reportUnreached   bool

	// gating and output
	failOnRegression bool
	failOnGap        bool
	format           string
	summaryFile      string

	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the check command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("check").SetParent(parent.Flags)
	cfg.bindMeasurementFlags()
	cfg.bindDecisionFlags()
	cfg.bindCoverageFlags()
	cfg.bindOutputFlags()
	cfg.Command = &ff.Command{
		Name:      "check",
		Usage:     "benchgate check [flags]",
		ShortHelp: "measure a base revision and HEAD, compare them, and report coverage gaps",
		LongHelp: `Measure the benchmarks at a base revision and at HEAD, decide whether any
difference is a regression, and report which code the benchmarks never reach.

The base revision is materialised in a throwaway git worktree, so your working
tree is never touched and the gate is safe to run on a dirty checkout.

By default the two revisions are measured alternately — base, head, base, head
— rather than all of one and then all of the other. On a shared CI runner,
thermal throttling and noisy neighbours drift over the life of a job; measuring
in blocks aligns that drift with the base/head distinction, so the statistics
blame the change for it. Alternating spreads it over both revisions, where it
cancels. Pass --interleave=false on a dedicated, quiet machine, where blocked
order is faster because each revision's build cache stays warm.

A difference has to clear two bars to count as a regression: the p-value must
be below --alpha, so the difference is unlikely to be noise, and the magnitude
must exceed --tolerance, so it is worth somebody's afternoon. Either bar alone
produces a gate people learn to ignore.

Nothing fails the build unless you ask. Add --fail-on-regression, and
--fail-on-gap once the reported gaps are ones you intend to keep clear.

Put [skip benchgate] in the head commit message to skip a run entirely.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) bindMeasurementFlags() {
	cfg.Flags.StringVar(&cfg.base, 0, "base", "HEAD~1",
		"git revision to compare HEAD against")
	cfg.Flags.StringVar(&cfg.packages, 0, "packages", gotest.DefaultPackages,
		"package pattern to benchmark")
	cfg.Flags.StringVar(&cfg.bench, 0, "bench", gotest.DefaultBench,
		"-bench regexp selecting which benchmarks to run")
	cfg.Flags.IntVar(&cfg.count, 0, "count", gotest.DefaultCount,
		"-count for each individual run; prefer --rounds, which interleaves")
	cfg.Flags.StringVar(&cfg.benchtime, 0, "benchtime", gotest.DefaultBenchtime,
		"-benchtime value, e.g. 2s or 100x; empty uses the go default")
	cfg.Flags.StringVar(&cfg.cpu, 0, "cpu", "",
		"-cpu list, e.g. 1,4; empty uses the current GOMAXPROCS")
	cfg.Flags.StringVar(&cfg.tags, 0, "tags", "",
		"build tags to pass to go test")
	cfg.Flags.BoolVarDefault(&cfg.benchmem, 0, "benchmem", true,
		"measure B/op and allocs/op, which are far less noisy than wall time")
	cfg.Flags.IntVar(&cfg.rounds, 0, "rounds", gate.DefaultRounds,
		"how many times to measure each revision")
	cfg.Flags.BoolVarDefault(&cfg.interleave, 0, "interleave", true,
		"alternate the revisions so runner drift affects both equally")
	cfg.Flags.DurationVar(&cfg.warmup, 0, "warmup", gate.DefaultWarmup,
		"run each revision for this long and discard it first; 0 disables")
	cfg.Flags.DurationVar(&cfg.cooldown, 0, "cooldown", gate.DefaultCooldown,
		"pause between consecutive runs so turbo headroom recovers; 0 disables")
}

func (cfg *Config) bindDecisionFlags() {
	cfg.Flags.Float64Var(&cfg.alpha, 0, "alpha", benchgate.DefaultAlpha,
		"p-value below which a difference counts as statistically significant")
	cfg.Flags.Float64Var(&cfg.tolerance, 0, "tolerance", benchgate.DefaultTolerance,
		"percentage a significant difference must exceed to count as a regression")
	cfg.Flags.Float64Var(&cfg.confidence, 0, "confidence", benchgate.DefaultConfidence,
		"confidence level for the interval reported around each measurement")
	cfg.Flags.StringListVar(&cfg.metrics, 0, "metric",
		"unit to gate on, e.g. ns/op or allocs/op; repeatable; default is every unit")
}

func (cfg *Config) bindCoverageFlags() {
	cfg.Flags.Float64Var(&cfg.minCoverage, 0, "min-bench-coverage", benchgate.DefaultMinCoverage,
		"floor for the percentage of statements the benchmarks execute; 0 disables")
	cfg.Flags.BoolVar(&cfg.requirePerPackage, 0, "require-benchmark-per-package",
		"report every package that declares no Benchmark function")
	cfg.Flags.BoolVar(&cfg.reportUnreached, 0, "report-unreached",
		"report every function no benchmark executes")
}

func (cfg *Config) bindOutputFlags() {
	cfg.Flags.BoolVar(&cfg.failOnRegression, 0, "fail-on-regression",
		"exit 2 when a regression is found")
	cfg.Flags.BoolVar(&cfg.failOnGap, 0, "fail-on-gap",
		"exit 3 when a benchmark coverage gap is found")
	cfg.Flags.StringEnumVar(&cfg.format, 0, "format",
		"output format", string(benchgate.FormatText),
		string(benchgate.FormatMarkdown), string(benchgate.FormatJSON))
	cfg.Flags.StringVar(&cfg.summaryFile, 0, "summary-file", "",
		"also append the markdown report to this file, e.g. $GITHUB_STEP_SUMMARY")
}

func (cfg *Config) exec(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("check: unexpected argument %q", args[0])
	}
	format, err := benchgate.ParseFormat(cfg.format)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}
	policy, err := benchgate.NewPolicy(
		benchgate.Significance{
			Alpha:      cfg.alpha,
			Tolerance:  cfg.tolerance,
			Confidence: cfg.confidence,
		},
		benchgate.Coverage{
			MinPercent:        cfg.minCoverage,
			RequirePerPackage: cfg.requirePerPackage,
			ReportUnreached:   cfg.reportUnreached,
		},
		benchgate.Gates{Regression: cfg.failOnRegression, Gap: cfg.failOnGap},
		cfg.metrics,
	)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}

	debug := cfg.DebugWriter()
	gateCheck := gate.Check{
		Repo:       gitwork.Repo{Dir: cfg.Dir, Debug: debug},
		Go:         gotest.Runner{Dir: cfg.Dir, Debug: debug},
		Bench:      cfg.benchOptions(),
		Policy:     policy,
		BaseRef:    cfg.base,
		Rounds:     cfg.rounds,
		Interleave: cfg.interleave,
		Warmup:     cfg.warmup,
		Cooldown:   cfg.cooldown,
	}
	report, err := gateCheck.Run(ctx)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}
	if err = gate.Emit(cfg.Stdout, cfg.summaryFile, &report, format); err != nil {
		return fmt.Errorf("check: %w", err)
	}
	// The report has already said what it found, so the exit code carries no
	// additional message. ExitError is what makes that distinction; os.Exit
	// here would skip main's signal cleanup.
	if code := report.ExitCode(); code != benchgate.ExitPass {
		return root.ExitError(code)
	}
	return nil
}

func (cfg *Config) benchOptions() gotest.Options {
	return gotest.Options{
		Packages:  cfg.packages,
		Bench:     cfg.bench,
		Count:     cfg.count,
		Benchtime: cfg.benchtime,
		CPU:       cfg.cpu,
		Tags:      cfg.tags,
		Benchmem:  cfg.benchmem,
	}
}
