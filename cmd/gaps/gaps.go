// Package gaps implements the "gaps" CLI command: benchmark coverage analysis
// with no comparison and no git.
package gaps

import (
	"context"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/benchgate/cmd/root"
	"github.com/StevenACoffman/benchgate/internal/benchgate"
	"github.com/StevenACoffman/benchgate/internal/gate"
	"github.com/StevenACoffman/benchgate/internal/gotest"
)

// Config holds the configuration for the gaps command.
type Config struct {
	*root.Config

	packages  string
	bench     string
	benchtime string
	tags      string

	minCoverage       float64
	requirePerPackage bool
	reportUnreached   bool

	failOnGap   bool
	format      string
	summaryFile string

	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the gaps command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("gaps").SetParent(parent.Flags)
	cfg.Flags.StringVar(&cfg.packages, 0, "packages", gotest.DefaultPackages,
		"package pattern to analyse")
	cfg.Flags.StringVar(&cfg.bench, 0, "bench", gotest.DefaultBench,
		"-bench regexp selecting which benchmarks to run")
	cfg.Flags.StringVar(&cfg.benchtime, 0, "benchtime", "1x",
		"-benchtime value; the default of 1x is enough, because coverage asks "+
			"which statements ran and not how fast")
	cfg.Flags.StringVar(&cfg.tags, 0, "tags", "",
		"build tags to pass to go test")
	cfg.Flags.Float64Var(&cfg.minCoverage, 0, "min-bench-coverage", benchgate.DefaultMinCoverage,
		"floor for the percentage of statements the benchmarks execute; 0 disables")
	cfg.Flags.BoolVarDefault(&cfg.requirePerPackage, 0, "require-benchmark-per-package", true,
		"report every package that declares no Benchmark function")
	cfg.Flags.BoolVarDefault(&cfg.reportUnreached, 0, "report-unreached", true,
		"report every function no benchmark executes")
	cfg.Flags.BoolVar(&cfg.failOnGap, 0, "fail-on-gap",
		"exit 3 when a benchmark coverage gap is found")
	cfg.Flags.StringEnumVar(&cfg.format, 0, "format",
		"output format", string(benchgate.FormatText),
		string(benchgate.FormatMarkdown), string(benchgate.FormatJSON))
	cfg.Flags.StringVar(&cfg.summaryFile, 0, "summary-file", "",
		"also append the markdown report to this file, e.g. $GITHUB_STEP_SUMMARY")
	cfg.Command = &ff.Command{
		Name:      "gaps",
		Usage:     "benchgate gaps [flags]",
		ShortHelp: "report which code the benchmarks never reach",
		LongHelp: `Run the benchmarks with coverage instrumentation and report what they fail to
reach. Nothing is compared and git is not consulted, so this answers a question
about the current tree on its own.

Three kinds of gap are reported:

  no-benchmark   a package that declares no Benchmark function at all. Whatever
                 it does, no measurement will ever notice it getting slower.
  unreached      a function the benchmark run never executes a statement of. A
                 suite that misses the branch a change touches cannot detect
                 that change's cost.
  below-floor    aggregate statement coverage under --min-bench-coverage.

Coverage is measured with -coverpkg over the whole package pattern, so a
statement a benchmark reaches by calling into another package counts. Without
that, every cross-package call reads as uncovered and no floor is meetable.

This is reported, not enforced, until you pass --fail-on-gap. "unreached" in
particular is noisy by design on a codebase with a lot of code that is not on a
hot path — it is the list of things no benchmark can speak for, which is a
reading list before it is a gate.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("gaps: unexpected argument %q", args[0])
	}
	format, err := benchgate.ParseFormat(cfg.format)
	if err != nil {
		return fmt.Errorf("gaps: %w", err)
	}
	policy, err := benchgate.NewPolicy(
		benchgate.Significance{
			Alpha:      benchgate.DefaultAlpha,
			Tolerance:  benchgate.DefaultTolerance,
			Confidence: benchgate.DefaultConfidence,
		},
		benchgate.Coverage{
			MinPercent:        cfg.minCoverage,
			RequirePerPackage: cfg.requirePerPackage,
			ReportUnreached:   cfg.reportUnreached,
		},
		benchgate.Gates{Gap: cfg.failOnGap},
		nil,
	)
	if err != nil {
		return fmt.Errorf("gaps: %w", err)
	}

	report := benchgate.NewReport(policy)
	if err = cfg.analyse(ctx, &report); err != nil {
		return fmt.Errorf("gaps: %w", err)
	}
	if err = gate.Emit(cfg.Stdout, cfg.summaryFile, &report, format); err != nil {
		return fmt.Errorf("gaps: %w", err)
	}
	if code := report.ExitCode(); code != benchgate.ExitPass {
		return root.ExitError(code)
	}
	return nil
}

// analyse fills in the coverage half of the report. The comparison half stays
// empty: there is nothing to compare.
func (cfg *Config) analyse(ctx context.Context, report *benchgate.Report) error {
	opts := gotest.Options{
		Packages:  cfg.packages,
		Bench:     cfg.bench,
		Benchtime: cfg.benchtime,
		Tags:      cfg.tags,
	}
	report.BenchCommand = opts.String()
	// Classify against the report's own policy rather than a second copy: the
	// exit code is judged by that one, and two copies can drift apart.
	prof, gaps, err := gate.AnalyseCoverage(
		ctx,
		gotest.Runner{Dir: cfg.Dir, Debug: cfg.DebugWriter()},
		&opts,
		report.Policy(),
	)
	if err != nil {
		return fmt.Errorf("analysing benchmark coverage: %w", err)
	}
	report.Coverage, report.Gaps = prof, gaps
	return nil
}
