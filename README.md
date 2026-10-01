# benchgate

Gate a pull request on Go benchmark regressions and on benchmark coverage gaps.

benchgate measures your benchmarks at a base revision and at HEAD, decides
whether a difference is both statistically real and large enough to matter, and
reports which of your code no benchmark ever reaches. It can block the build on
either finding, and it blocks on neither unless you ask.

```text
benchmark gate: origin/main (4f6da8073fee) -> HEAD (e9483817fb6a)
command:  go test ./... -run ^$ -bench . -count 1 -benchmem
package:  github.com/you/yourmod/tokenize
platform: linux/amd64
cpu:      AMD EPYC 7763
policy:   alpha 0.05, tolerance 5%

BENCHMARK           UNIT       BASE          HEAD          DELTA     P       N  VERDICT
BenchmarkFields-8   ns/op      19.15µ ± 6%   11.53µ ± 2%   -39.76%   0.002   6  improved
BenchmarkFields-8   B/op       9.250Ki ± 0%  18.31Ki ± 0%  +97.97%   0.002   6  regressed
BenchmarkFields-8   allocs/op  1.000 ± 0%    8.000 ± 0%    +700.00%  0.002   6  regressed

2 regressed, 1 improved, 0 unchanged, 0 unmeasurable, 0 added, 0 removed
exit code: 2
```

## Install

```sh
go install github.com/StevenACoffman/benchgate@latest
```

No other tools. There is no `benchstat` to install either: the statistics run
in-process on `golang.org/x/perf/benchmath`, the library that benchstat is
itself built on.

## GitHub Actions

```yaml
- uses: actions/checkout@v5
  with:
    fetch-depth: 0          # the gate needs history to measure the base revision
- uses: actions/setup-go@v5
  with:
    go-version-file: go.mod
- uses: StevenACoffman/benchgate@v0
  with:
    metrics: B/op allocs/op
    tolerance: '0'
    fail-on-regression: true
```

Start without `fail-on-regression`. You get a table in the job summary and an
exit code of 0, so you can read a few weeks of real reports before deciding what
should block. A gate that is red on arrival gets switched off rather than fixed.

See [`_example/`](./_example) for a complete working module, a `justfile`
covering every local invocation, and the workflow to copy.

## Commands

| Command                                   | What it does                                                                              |
| ----------------------------------------- | ----------------------------------------------------------------------------------------- |
| `benchgate check`                         | Measure a base revision and HEAD, compare them, report coverage gaps. The CI entry point. |
| `benchgate compare <base.txt> <head.txt>` | Run the statistics over two result files that already exist, without touching git.        |
| `benchgate gaps`                          | Report what the benchmarks never reach, without comparing revisions.                      |
| `benchgate version`                       | Build and version information.                                                            |

`benchgate <command> --help` prints the full flag reference. Every flag also
reads from a `BENCHGATE_`-prefixed environment variable. Uppercase the name and
put an underscore where each dash was, which makes `--tolerance` into
`BENCHGATE_TOLERANCE`. A flag on the command line always wins.

## Exit codes

| Code | Meaning                                                           |
| ---- | ----------------------------------------------------------------- |
| 0    | Every enabled gate passed. Findings may still have been reported. |
| 1    | The run could not finish. Blame git, the go command or the input. |
| 2    | A regression was found and `--fail-on-regression` was set.        |
| 3    | A coverage gap was found and `--fail-on-gap` was set.             |

2 and 3 stay distinct from 1 on purpose. "The tool broke" and "the tool worked
and your code got slower" are different problems with different owners, and a CI
job should be able to tell them apart without parsing a log.

## How it decides

**A regression has to clear two bars, not one.** The p-value must fall below
`--alpha` (default 0.05), so the difference is unlikely to be noise, and the
magnitude must exceed `--tolerance` (default 5%), so it is worth somebody's
afternoon. Significance on its own makes the gate chase 0.4% jitter. Magnitude
on its own makes it fail builds for noise. Tools that check only one of the two
are why people stop trusting performance gates.

**Direction comes from the unit.** Lower is better for `ns/op`, and higher is
better for `B/s`. A gate that reads every rise as a degradation fails your build
for a 30% throughput win. benchgate looks up the improvement direction per unit,
honouring a `better=higher` unit-metadata line when your benchmark emits one. A
unit it has no direction for never produces a regression at all. It warns
instead, naming the line that would teach it.

**Both revisions get measured alternately:** base, head, base, head, rather than
all of one and then all of the other. On a shared CI runner, thermal throttling
and noisy neighbours drift over the life of a job. Measuring in blocks lines
that drift up exactly with the base-versus-head distinction, so the statistics
attribute it to your change. Alternating spreads it over both arms, where it
cancels out. This is the single largest source of false regressions in a naive
gate. Pass `--interleave=false` on a dedicated, quiet machine, where blocked
order runs faster because each arm's build cache stays warm.

**The base revision never touches your working tree.** It gets checked out into
a throwaway `git worktree`, which makes the gate safe to run on a dirty checkout
and unable to lose uncommitted work. That is also what makes `--base HEAD` the
way to measure a change you have not committed yet.

**A measurement too noisy to trust says so.** When a confidence interval comes
out wider than ±10% of its own center, the comparison gets a warning that names
the fix. Statistics applied correctly to measurements that were never stable
enough to carry them is how a gate produces confident nonsense.

**"Not enough samples" is not "no regression".** When the sample is too small
for the requested alpha, the verdict reads `unmeasurable` and the gate does not
block, because no information is not evidence of safety. Fewer than four rounds
per arm cannot produce a result below the default alpha, whatever changed.

## Benchmark coverage gaps

A delta report only speaks about code a benchmark actually runs. `go test -cover`
measures your *tests*, which answers a different question. benchgate runs the
benchmarks on their own, passing `-run '^$'` so that no test contributes, with
`-coverpkg` across the whole pattern. It reports three kinds of gap:

| Kind           | Meaning                                                                                                                                |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `no-benchmark` | A package declaring no `Benchmark` function at all. Nothing will ever notice it getting slower.                                        |
| `unreached`    | A function the benchmark run never executes a statement of. A suite that misses the branch your change touches cannot detect its cost. |
| `below-floor`  | Aggregate benchmark-only statement coverage under `--min-bench-coverage`.                                                              |

`-coverpkg` is not optional here. Without it a benchmark instruments only its own
package, so every cross-package call reads as uncovered and no floor can be met.

Leave `--min-bench-coverage` at 0 and look at your real number first.
`--report-unreached` is deliberately noisy on a codebase with a lot of code that
is not on a hot path. Treat it as a reading list before you treat it as a gate.

## Which metrics to gate on

Gate allocations first. `B/op` and `allocs/op` barely move with runner load, so
a change in them reflects a change in your code rather than in the weather, and
`--tolerance 0` on them is not flaky. Wall time behaves nothing like that. Gate
it later, with a loose tolerance, once you know how noisy your runners really
are.

```sh
benchgate check --metric B/op --metric allocs/op --tolerance 0 --fail-on-regression
```

## Escape hatch

Put `[skip benchgate]` in a commit message and the gate skips that commit,
reporting why. The hatch has to exist and has to be documented. The first time a
gate is wrong about something urgent, the alternative is somebody deleting the
whole job.

## Machine-readable output

`--format json` is a documented contract. Every comparison includes its p-value,
its delta, its sample counts and its warnings, so a consumer can apply its own
thresholds, and the exit code implied by the policy is in there too so that
nothing has to reimplement the decision.

`--format markdown` suits a job summary or a pull-request comment.
`--summary-file` writes the Markdown to a file *in addition to* whatever
`--format` puts on stdout, because a job log and a review comment have different
readers.

## Prior art

benchgate exists because of what these get right and what they get wrong.

- [`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) is the
  statistics, and benchgate uses the same library. It is not a gate: it prints a
  table and exits 0.
- [`cob`](https://github.com/knqyf263/cob) pioneered the shape, comparing HEAD
  with its parent and failing on a threshold, and contributed the `[skip]`
  escape hatch. It does no statistics at all, so a single noisy run becomes the
  verdict, and it reaches the base revision with `git reset --hard`, which
  destroys uncommitted work.
- [`benchdiff`](https://github.com/WillAbides/benchdiff) contributed the
  `git worktree` approach used here, along with warmups and a degradation exit
  code separate from failure. It is built on `golang.org/x/perf/benchstat`,
  which upstream has since deprecated. It is the closest relative benchgate
  has, so [the addendum](#addendum-benchgate-and-benchdiff) compares the two in
  detail.

None of the three measures the two arms alternately, and none reports what the
benchmarks fail to reach.

## Development

```sh
just          # list every recipe
just test     # unit suite, race detector on
just lint     # golangci-lint
just check    # every gate CI runs
just example  # build, test and lint the nested _example module
```

The layout follows a functional-core and imperative-shell split, enforced by
`depguard` rather than by review:

```text
main.go                  run() translates errors into exit codes
cmd/                     one package per command; flags bound in New()
internal/benchgate/      the pure core: parsing, statistics, verdicts, rendering
internal/gotest/         adapter for the go toolchain
internal/gitwork/        adapter for git worktrees
internal/gate/           the shell: composes the adapters and the core
_example/                a nested module the gate is dogfooded against in CI
```

Nothing in `internal/benchgate` touches a subprocess, the filesystem or the
clock. That is what lets its statistics be tested as a pure function of two
slices of floats, instead of by running benchmarks.

## Addendum: benchgate and benchdiff

[`benchdiff`](https://github.com/WillAbides/benchdiff) is prior art, and the
closest relative benchgate has. benchgate took its central good idea: materialise
the base revision with `git worktree add --detach` into a temporary directory, so
the user's working tree stays untouched. That beats `cob`'s `git reset --hard`,
which destroys uncommitted work. benchgate also inherited benchdiff's warmup
runs, its cooldown pause between runs, and its decision to give a degradation its
own exit code separate from an operational failure.

Both tools also make a regression clear two bars rather than one. benchdiff gets
significance from old benchstat, whose `Row.Change` is only set when
`pval < alpha`, and magnitude from its own `--tolerance`. That is worth stating
plainly, because a single-bar check is the defect most performance gates have and
benchdiff does not have it.

### The difference that matters most

Result caching and interleaved measurement cannot both exist in one tool, and
that is the real fork between these two.

benchdiff caches the base measurement as `benchdiff-<baseSHA>-<cacheKey>.out` and
skips the base run outright when that file is already present. On a busy
repository where many pull requests share one base, this roughly halves what CI
spends.

benchgate cannot cache, because it alternates the two revisions: base, head,
base, head. A cached base measurement was taken at some other time, possibly on
some other runner, which is the exact confound that alternating exists to remove.
benchdiff's own order is blocked (`warmup(base)`, then all of base, then all of
head), so even an uncached run leaves runner drift aligned with the
base-versus-head distinction.

benchdiff is cheaper to run and benchgate's numbers are easier to defend. On a
dedicated, quiet machine benchdiff's trade is the better one, which is also why
benchgate has `--interleave=false`.

### Differences in detail

|                                      | benchdiff                                                                                     | benchgate                                                         |
| ------------------------------------ | --------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| x/perf API                           | `benchstat`, marked **Deprecated** upstream                                                   | `benchfmt`, `benchmath`, `benchunit`, the recommended replacement |
| Measurement order                    | Blocked: all of base, then all of head                                                        | Alternating by default, with `--interleave=false` to opt out      |
| Base result caching                  | Yes, keyed by SHA and command                                                                 | No, so every run measures both revisions                          |
| Unit direction                       | Hardcoded as `pct < 0 == (metric != "speed")`                                                 | `GetBetter(unit)` per unit, honouring a `better=` metadata line   |
| "Could not tell" versus "no change"  | Collapsed: too few samples, identical samples and a p-value over alpha all become `Change: 0` | A separate `unmeasurable` verdict, reported and never blocking    |
| New or deleted benchmarks            | Dropped from the report without comment                                                       | `added` and `removed` verdicts                                    |
| Inputs from different platforms      | Compared anyway                                                                               | Refused with `EINVALID`                                           |
| Noisy-measurement warning            | None                                                                                          | Warns when a confidence interval exceeds ±10% of its center       |
| Benchmark coverage gaps              | Absent: no `-coverprofile`, `-coverpkg` or `-list` anywhere in the repository                 | Package, function and floor gaps, plus a `gaps` subcommand        |
| Exit codes                           | One configurable code, `--on-degrade=N`                                                       | Fixed 1, 2 and 3 for operational failure, regression and gap      |
| Skip marker                          | None                                                                                          | `[skip benchgate]` in a commit message                            |
| Arbitrary benchmark command          | Yes, via `--benchmark-cmd` and a templated `--benchmark-args`, so `make bench` works          | No. Typed flags only, with `compare` as the escape hatch          |
| Output formats                       | text, csv, html, markdown, json                                                               | text, markdown, json                                              |
| benchstat presentation knobs         | `--sort`, `--split`, `--geomean`, `--norange`, `--delta-test=ttest\|none`                     | None of these                                                     |
| Benchmarking the Go standard library | Special-cased, including a `make.bash` rebuild                                                | Not supported                                                     |

That fourth row deserves the source. In the two-revision mode benchdiff uses,
old benchstat drops any benchmark present on only one side.

```go
// If one is missing, omit row entirely.
// TODO: Control this better.
if old == nil || new == nil {
    continue
}
```

So a benchmark somebody deleted leaves no trace in the report. That is the
finding benchgate's `removed` verdict exists to surface.

### Where benchdiff is the better tool

Caching, the `make bench` escape hatch, CSV and HTML output, the benchstat
presentation knobs, standard-library support, a published GitHub Action, bindown
packaging, and a track record of people using it. benchgate dropped the templated
argument flexibility on purpose, under the rule that one way to say a thing beats
two, and it never had caching at all. Both of those are costs rather than wins.

### One probable bug worth knowing about

```go
func (r *RunResult) maxDegradedPct() float64 {
    max := 0.0
    // ... if row.Change != DegradingChange { continue }
    if row.PctDelta > max { max = row.PctDelta }
```

For a throughput metric such as `MB/s`, which old benchstat names `"speed"`, a
degradation means `PctDelta` is negative. A negative value can never exceed
`max`, which starts at `0.0`. So old benchstat correctly sets `Change: -1` on a
throughput regression and then `HasDegradedResult` discards it, which would leave
`--on-degrade` silent about a drop in `MB/s`. This comes from reading the source
rather than from running it, so treat it as probable rather than confirmed.

It mirrors the defect in the design notes benchgate started from. That program
failed builds for throughput *wins*, and this one appears to miss throughput
*losses*. Both come of treating the sign of a number as the sign of the news. So
benchgate resolves the improvement direction per unit, and has a test asserting
that a rise in `MB/s` reads as an improvement.
