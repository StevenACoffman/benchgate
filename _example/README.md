# benchgate example

A small Go module, benchmarked, with benchgate wired into CI against it.

It is a nested module behind an underscore, so the parent module's `./...` never
walks into it. Everything here runs on its own, and nothing here imports
benchgate. benchgate is a command you run *against* a module, not a library you
build into one, so the example usage lives in the `justfile` and in
`.github/workflows/perf.yaml` rather than in an import.

## What is here, and why each piece exists

| Path                         | What it is for                                                                                                                        |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `tokenize/tokenize.go`       | Hot paths of deliberately different shapes. One never allocates, the next allocates once per call, the last allocates once per token. |
| `tokenize/tokenize_test.go`  | Tests, plus benchmarks covering `ns/op`, `B/op`, `allocs/op` and `B/s`, including a sub-benchmark grid.                               |
| `tokenize.LongestToken`      | A function with **no benchmark**, so `benchgate gaps` has a true `unreached` finding to report.                                       |
| `internal/render/`           | A package with tests but **no benchmarks at all**, which is what `--require-benchmark-per-package` reports.                           |
| `justfile`                   | Every way you would invoke the gate locally.                                                                                          |
| `.github/workflows/perf.yaml` | The workflow to copy into your own repository root.                                                                                   |

Those gaps are not oversights. A tool that reports an empty list against its own
example teaches you nothing about what a real finding looks like.

## Try it

Everything needs the binary, which `just build` builds from the parent module:

```sh
just build
```

### See what the gate says, without it blocking anything

```sh
just check                 # HEAD against HEAD~1
just check origin/main     # or against a branch
```

Out comes a table of measurements with a verdict against each, under an exit
code of 0 that fails nothing. This is where to start on a real project, because
reading a few weeks of these is how you learn what should block.

### Watch it catch a real regression

```sh
just demo
```

That rewrites `Fields` to let `append` grow the slice instead of presizing it,
which gives the same answer for eight allocations instead of one. It measures
the working tree against the last commit and then puts the file back. The run
ends with `exit code: 2` and rows like these:

```text
BENCHMARK            UNIT       BASE          HEAD          DELTA     P       N  VERDICT
BenchmarkFields-10   B/op       9.250Ki ± 0%  18.31Ki ± 0%  +97.97%   0.002   6  regressed
BenchmarkFields-10   allocs/op  1.000 ± 0%    8.000 ± 0%    +700.00%  0.002   6  regressed
```

Look at what the `ns/op` row says in that same run: *faster*. Growing a small
slice with `append` costs less than a second pass over the input to count the
tokens first. The change buys time with memory, so a gate watching only the
clock would have waved it through. That is the argument for gating on
allocations.

`--base HEAD` is what makes this work on an uncommitted change. The base
revision gets measured in a throwaway worktree while the head arm gets measured
in your actual working tree, so you can check a change before committing it.

### Block the build on an allocation regression

```sh
just check-allocs
```

That runs `--metric allocs/op --metric B/op --tolerance 0 --fail-on-regression`.

Allocation counts make the right first gate on a shared CI runner. They barely
move with runner load, so a change in them reflects a change in the code rather
than in the weather, which makes a zero tolerance on them safe. Wall time
behaves nothing like that. Gate it later, with a loose tolerance, once you know
how noisy your runners really are.

### Find what the benchmarks never reach

```sh
just gaps
```

Reports every true finding against this module:

```text
benchmark coverage gaps (3):
  .../_example/internal/render: package declares no Benchmark function
  .../_example/internal/render/render.go:12: Summary: no benchmark executes any statement in this function
  .../_example/tokenize/tokenize.go:97: LongestToken: no benchmark executes any statement in this function
```

This is the half of the problem a delta report cannot see. A benchmark suite
that never touches the branch your change modifies cannot detect that change's
cost, and the suite passing tells you nothing about it.

### Compare two files the gate never measured

```sh
just bench base.txt
# ... change something ...
just bench head.txt
just compare base.txt head.txt
```

Useful, with one caveat worth taking seriously. A file measured yesterday and a
file measured on another machine were measured under conditions that differed,
so a p-value computed across them describes a difference between environments
rather than between revisions. That is why `compare` is a separate command
instead of a flag on `check`. You have to ask for it on purpose.

## Wiring it into your own repository

Copy `.github/workflows/perf.yaml` to your repository root. Watch for these.

- **`fetch-depth: 0` on `actions/checkout`.** The gate adds a detached git
  worktree at the base revision to measure it, and the default checkout depth of
  1 leaves no history to do that with. This is the most common
  misconfiguration. The action notices it and deepens the repository itself, but
  doing it in the checkout is faster.
- **Pin the action to a commit SHA.** An unpinned gate can change its own
  verdicts once a new release is published, and a build that starts failing for
  that reason looks exactly like a real regression.
