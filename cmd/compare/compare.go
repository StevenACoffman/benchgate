// Package compare implements the "compare" CLI command: the statistics on two
// result files that already exist.
package compare

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/benchgate/cmd/root"
	"github.com/StevenACoffman/benchgate/internal/benchgate"
	"github.com/StevenACoffman/benchgate/internal/gate"
)

// Config holds the configuration for the compare command.
type Config struct {
	*root.Config

	alpha            float64
	tolerance        float64
	confidence       float64
	metrics          []string
	failOnRegression bool
	format           string
	summaryFile      string

	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the compare command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("compare").SetParent(parent.Flags)
	cfg.Flags.Float64Var(&cfg.alpha, 0, "alpha", benchgate.DefaultAlpha,
		"p-value below which a difference counts as statistically significant")
	cfg.Flags.Float64Var(&cfg.tolerance, 0, "tolerance", benchgate.DefaultTolerance,
		"percentage a significant difference must exceed to count as a regression")
	cfg.Flags.Float64Var(&cfg.confidence, 0, "confidence", benchgate.DefaultConfidence,
		"confidence level for the interval reported around each measurement")
	cfg.Flags.StringListVar(&cfg.metrics, 0, "metric",
		"unit to gate on, e.g. ns/op or allocs/op; repeatable; default is every unit")
	cfg.Flags.BoolVar(&cfg.failOnRegression, 0, "fail-on-regression",
		"exit 2 when a regression is found")
	cfg.Flags.StringEnumVar(&cfg.format, 0, "format",
		"output format", string(benchgate.FormatText),
		string(benchgate.FormatMarkdown), string(benchgate.FormatJSON))
	cfg.Flags.StringVar(&cfg.summaryFile, 0, "summary-file", "",
		"also append the markdown report to this file, e.g. $GITHUB_STEP_SUMMARY")
	cfg.Command = &ff.Command{
		Name:      "compare",
		Usage:     "benchgate compare [flags] <base-file> <head-file>",
		ShortHelp: "compare two existing `go test -bench` result files",
		LongHelp: `Run the statistics over two files of ` + "`go test -bench`" + ` output that already
exist. Nothing is measured and git is not consulted, so this works anywhere —
including on files produced on another machine, at another time, or by a
` + "`make bench`" + ` recipe benchgate knows nothing about.

That portability is also the catch, and the reason this is a separate command
rather than a flag on check. Two files measured on different runners were
measured under different conditions, and a p-value computed across them answers
a question nobody asked: it reports a confident difference between two
machines. Use this when you know that and accept it. When you want a number you
can act on, use ` + "`benchgate check`" + `, which measures both revisions on one
machine, alternately, in one job.

Produce input files with, for example:

    go test -run '^$' -bench . -benchmem -count 10 ./... > base.txt

The two files must have been measured on the same goos and goarch; comparing
across platforms is refused rather than reported.

Note the argument order: flags come before the two file names. Flag parsing
stops at the first argument that is not a flag, so anything after the file names
is read as a third file rather than as an option.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(_ context.Context, args []string) error {
	if err := checkArgs(args); err != nil {
		return err
	}
	format, err := benchgate.ParseFormat(cfg.format)
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}
	policy, err := benchgate.NewPolicy(
		benchgate.Significance{
			Alpha:      cfg.alpha,
			Tolerance:  cfg.tolerance,
			Confidence: cfg.confidence,
		},
		benchgate.Coverage{},
		benchgate.Gates{Regression: cfg.failOnRegression},
		cfg.metrics,
	)
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}

	base, err := readSeries(args[0])
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}
	head, err := readSeries(args[1])
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}
	comparisons, err := benchgate.Compare(base, head, policy)
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}

	report := benchgate.NewReport(policy)
	// There is no commit behind a file, so the file name is the whole identity
	// of each side.
	report.Base = benchgate.Revision{Ref: args[0]}
	report.Head = benchgate.Revision{Ref: args[1]}
	report.Platform, report.CPU = head.Platform(), head.CPU()
	report.Comparisons = comparisons
	if err = gate.Emit(cfg.Stdout, cfg.summaryFile, &report, format); err != nil {
		return fmt.Errorf("compare: %w", err)
	}
	if code := report.ExitCode(); code != benchgate.ExitPass {
		return root.ExitError(code)
	}
	return nil
}

// checkArgs rejects the wrong number of file names, and recognises the one
// mistake this command invites: flag parsing stops at the first non-flag
// argument, so flags written after the file names arrive here as extra files.
// Saying so is the difference between a one-line fix and a puzzle.
func checkArgs(args []string) error {
	const wantArgs = 2
	if len(args) == wantArgs {
		return nil
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf(
				"compare: %q was read as a file name, not a flag: "+
					"flags must come before the two file names", arg)
		}
	}
	return fmt.Errorf("compare: need exactly two result files, got %d", len(args))
}

func readSeries(path string) (benchgate.Series, error) {
	f, err := os.Open(path)
	if err != nil {
		// os.Open's message already names the path and the reason, and
		// ENOTFOUND lets a caller tell a missing file from a malformed one.
		return benchgate.Series{}, benchgate.Errorf(benchgate.ENOTFOUND,
			"reading %s: %s", path, err)
	}
	defer func() { _ = f.Close() }()
	series, err := benchgate.ParseResults(f, path)
	if err != nil {
		return benchgate.Series{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return series, nil
}
