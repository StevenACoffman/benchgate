// Package gate composes the adapters and the domain into one gate run.
//
// It is the imperative shell (summary_rules §5): a flat sequence of
// subprocess calls and file writes, which hands the bytes it collects to
// internal/benchgate and does no deciding of its own. It is the one package
// permitted to import both adapters, because composing them is its entire job.
package gate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
	"github.com/StevenACoffman/benchgate/internal/gitwork"
	"github.com/StevenACoffman/benchgate/internal/gotest"
)

// Defaults for the sampling plan. Exported so the flag definitions and the
// documentation share one source of truth.
const (
	// DefaultRounds is how many times each arm is measured. Six gives benchmath
	// enough samples to reject the null hypothesis at alpha 0.05 and to put a
	// confidence interval around each center, which four does not.
	DefaultRounds = 6
	// DefaultWarmup is how long to run each arm, discarding the results, before
	// the measured rounds begin. It is what populates the build cache and lets
	// the CPU settle, so the first measured round is not systematically the
	// slowest.
	DefaultWarmup = time.Second
	// DefaultCooldown is the pause between consecutive measured runs, giving
	// turbo headroom a chance to recover so a later round is not penalised for
	// following an earlier one.
	DefaultCooldown = 100 * time.Millisecond
	// SkipMarker in the head commit message skips the gate entirely. The escape
	// hatch has to exist, or the first time the gate is wrong about something
	// urgent the whole job gets deleted instead.
	SkipMarker = "[skip benchgate]"
)

// Check is one configured gate run.
//
// Construct it, then call Run. Every field is set from a flag, so there is no
// hidden configuration and nothing read from the environment behind the
// operator's back (summary_rules §18).
type Check struct {
	// Repo reaches git. Its Dir is the module to benchmark.
	Repo gitwork.Repo
	// Go reaches the go toolchain. Its Dir is the module to benchmark.
	Go gotest.Runner
	// Bench is how to invoke `go test -bench`.
	Bench gotest.Options
	// Policy is the validated decision rule the findings are judged against.
	Policy benchgate.Policy
	// BaseRef is the revision to compare against.
	BaseRef string
	// Rounds is how many times each arm is measured.
	Rounds int
	// Interleave alternates the arms — base, head, base, head — instead of
	// measuring all of base and then all of head.
	//
	// This is the difference between a trustworthy gate and a noise generator.
	// On a shared CI runner, thermal throttling and noisy neighbours drift over
	// the life of a job; measuring the arms in blocks aligns that drift exactly
	// with the base/head distinction, so the statistics attribute it to the
	// change. Alternating spreads it across both arms, where it cancels.
	// Blocked order is faster, because each arm's build cache stays warm, and
	// it is the right choice on a dedicated, quiet machine.
	Interleave bool
	// Warmup runs each arm for this long and discards the result before the
	// measured rounds. Zero skips it.
	Warmup time.Duration
	// Cooldown pauses between consecutive measured runs. Zero skips it.
	Cooldown time.Duration
}

// revPair is the resolved pair of commits a run compares.
type revPair struct {
	base, head benchgate.Revision
}

// Run measures both revisions, compares them, analyses benchmark coverage and
// returns the findings.
//
// Requires: Policy was returned by benchgate.NewPolicy; Repo.Dir and Go.Dir
// name the same module, inside a git repository.
// Ensures:  the caller's working tree is never modified; the returned Report
// carries Policy, so its ExitCode is the gate's answer; a head commit message
// containing SkipMarker yields an empty Report with a notice and no measurement.
func (c *Check) Run(ctx context.Context) (benchgate.Report, error) {
	report := benchgate.NewReport(c.Policy)

	revs, skip, err := c.revisions(ctx)
	if err != nil {
		return report, err
	}
	report.Base, report.Head = revs.base, revs.head
	report.BenchCommand = c.Bench.String()
	if skip != "" {
		report.Notices = append(report.Notices, skip)
		return report, nil
	}

	base, head, err := c.measure(ctx, revs)
	if err != nil {
		return report, err
	}
	report.Platform, report.CPU = head.Platform(), head.CPU()
	if report.Platform == "" {
		report.Platform, report.CPU = base.Platform(), base.CPU()
	}
	report.Notices = append(report.Notices, sameCommitNotice(revs.base, revs.head)...)
	report.Notices = append(report.Notices, emptyArmNotices(base, head)...)

	comparisons, err := benchgate.Compare(base, head, c.Policy)
	if err != nil {
		return report, fmt.Errorf("gate.Check.Run: %w", err)
	}
	report.Comparisons = comparisons

	// Only HEAD is instrumented: the question is what the benchmark suite
	// covers now, which is a property of the proposed code rather than of a
	// comparison.
	if c.Policy.ChecksCoverage() {
		prof, gaps, covErr := AnalyseCoverage(ctx, c.Go, &c.Bench, c.Policy)
		if covErr != nil {
			return report, fmt.Errorf("gate.Check.Run: %w", covErr)
		}
		report.Coverage, report.Gaps = prof, gaps
	}
	return report, nil
}

// revisions resolves both sides and reports whether the head commit asked to be
// skipped. A non-empty skip string is the notice to show.
func (c *Check) revisions(ctx context.Context) (revs revPair, skip string, err error) {
	headSHA, err := c.Repo.Resolve(ctx, "HEAD")
	if err != nil {
		return revs, "", fmt.Errorf("gate.Check.Run: resolving HEAD: %w", err)
	}
	baseSHA, err := c.Repo.Resolve(ctx, c.BaseRef)
	if err != nil {
		return revs, "", fmt.Errorf("gate.Check.Run: resolving the base ref: %w", err)
	}
	revs = revPair{
		base: benchgate.Revision{Ref: c.BaseRef, SHA: baseSHA},
		head: benchgate.Revision{Ref: "HEAD", SHA: headSHA},
	}

	msg, err := c.Repo.CommitMessage(ctx, "HEAD")
	if err != nil {
		return revs, "", fmt.Errorf("gate.Check.Run: %w", err)
	}
	if strings.Contains(strings.ToLower(msg), strings.ToLower(SkipMarker)) {
		return revs, "the head commit message contains " + SkipMarker + ", so no benchmarks were run", nil
	}

	return revs, "", nil
}

// sameCommitNotice explains a run whose two revisions are the same commit.
//
// This must not skip the run. The head arm is measured in the working tree,
// which can differ from HEAD, so `--base HEAD` is the useful way to measure an
// uncommitted change — and skipping would silently do nothing in exactly the
// case a developer reaches for first. On a freshly branched pull request the
// same situation means there is genuinely nothing to compare, and every row
// reading "unchanged" is then worth explaining.
func sameCommitNotice(base, head benchgate.Revision) []string {
	if base.SHA == "" || base.SHA != head.SHA {
		return nil
	}
	return []string{
		"the base ref and HEAD are the same commit, so any difference below " +
			"comes from uncommitted changes in the working tree",
	}
}

// measure collects the samples for both arms.
func (c *Check) measure(
	ctx context.Context,
	revs revPair,
) (base, head benchgate.Series, err error) {
	// One file per arm, appended to across rounds, then parsed once. This is
	// also the format `benchstat` and `benchgate compare` read, so a debugging
	// operator can keep them.
	dir, err := os.MkdirTemp("", "benchgate-results-")
	if err != nil {
		return base, head, fmt.Errorf("gate.Check.Run: creating a results directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	basePath := filepath.Join(dir, "base.txt")
	headPath := filepath.Join(dir, "head.txt")

	// The base revision is materialised in a throwaway worktree for the whole
	// measurement, so it is created and built once rather than once per round.
	if err = c.Repo.At(ctx, revs.base.Ref, func(baseDir string) error {
		return c.sample(ctx, baseDir, basePath, headPath)
	}); err != nil {
		return base, head, fmt.Errorf("gate.Check.Run: measuring %s: %w", revs.base.Ref, err)
	}

	base, err = parseFile(basePath, "base")
	if err != nil {
		return base, head, err
	}
	head, err = parseFile(headPath, "head")
	return base, head, err
}

// sample runs the warmup and then the measured rounds, appending each round's
// output to the file for its arm.
func (c *Check) sample(ctx context.Context, baseDir, basePath, headPath string) error {
	baseRunner, headRunner := c.Go, c.Go
	baseRunner.Dir = baseDir

	if c.Warmup > 0 {
		// A warmup run's numbers are discarded; its purpose is a populated
		// build cache and a CPU that has stopped idling.
		warm := c.Bench
		warm.Benchtime = c.Warmup.String()
		warm.Count = 1
		for _, r := range []gotest.Runner{baseRunner, headRunner} {
			if _, err := r.Bench(ctx, &warm); err != nil {
				return fmt.Errorf("warming up: %w", err)
			}
		}
	}

	rounds := c.rounds()
	if !c.Interleave {
		if err := c.blockedRounds(ctx, baseRunner, basePath, rounds); err != nil {
			return err
		}
		return c.blockedRounds(ctx, headRunner, headPath, rounds)
	}
	for round := range rounds {
		if err := c.round(ctx, baseRunner, basePath, round > 0); err != nil {
			return err
		}
		if err := c.round(ctx, headRunner, headPath, true); err != nil {
			return err
		}
	}
	return nil
}

// blockedRounds measures one arm repeatedly, for the non-interleaved order.
func (c *Check) blockedRounds(
	ctx context.Context,
	runner gotest.Runner,
	path string,
	rounds int,
) error {
	for round := range rounds {
		if err := c.round(ctx, runner, path, round > 0); err != nil {
			return err
		}
	}
	return nil
}

// round runs the benchmarks once and appends the output to path.
func (c *Check) round(
	ctx context.Context,
	runner gotest.Runner,
	path string,
	cooldown bool,
) error {
	if cooldown {
		if err := c.pause(ctx); err != nil {
			return err
		}
	}
	out, err := runner.Bench(ctx, &c.Bench)
	if err != nil {
		return fmt.Errorf("measuring: %w", err)
	}
	return appendFile(path, out)
}

// pause waits out the cooldown, or returns early if the run is cancelled.
//
// A select on time.After rather than time.Sleep: a sleep ignores cancellation,
// so Ctrl-C during a long run would be noticed only after the current pause
// expired, and the signal context main.go builds would reach nothing.
func (c *Check) pause(ctx context.Context) error {
	if c.Cooldown <= 0 {
		return nil
	}
	timer := time.NewTimer(c.Cooldown)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("cooling down: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (c *Check) rounds() int {
	if c.Rounds <= 0 {
		return DefaultRounds
	}
	return c.Rounds
}

// emptyArmNotices says so when an arm produced no measurements. Compare treats
// that as "everything was added" or "everything was removed", which is correct
// but opaque; a notice names the cause.
func emptyArmNotices(base, head benchgate.Series) []string {
	var notices []string
	if base.Len() == 0 {
		notices = append(
			notices,
			"the base revision produced no benchmark results; check that it has benchmarks matching -bench",
		)
	}
	if head.Len() == 0 {
		notices = append(notices,
			"HEAD produced no benchmark results; check that it has benchmarks matching -bench")
	}
	return notices
}

func parseFile(path, label string) (benchgate.Series, error) {
	f, err := os.Open(path)
	if err != nil {
		return benchgate.Series{}, fmt.Errorf(
			"gate.Check.Run: opening the %s results: %w",
			label,
			err,
		)
	}
	defer func() { _ = f.Close() }()
	series, err := benchgate.ParseResults(f, label)
	if err != nil {
		return benchgate.Series{}, fmt.Errorf("gate.Check.Run: %w", err)
	}
	return series, nil
}

func appendFile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}
