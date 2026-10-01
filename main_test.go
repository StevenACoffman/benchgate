package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive run() — the exit-code translator — the way main does, with
// injected I/O (summary_rules §18 Pattern B). They are the only place the whole
// stack is exercised together: flag parsing, git, the go toolchain, the
// statistics and the exit code.

// The shape of the JSON contract, declared here rather than imported, so a
// change to the real DTO that breaks a consumer breaks this test too. Only the
// fields asserted on are listed.
type reportJSON struct {
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Comparisons []struct {
		Name         string   `json:"name"`
		Unit         string   `json:"unit"`
		Verdict      string   `json:"verdict"`
		DeltaPercent *float64 `json:"delta_percent"`
		P            *float64 `json:"p"`
		SamplesBase  int      `json:"samples_base"`
		SamplesHead  int      `json:"samples_head"`
	} `json:"comparisons"`
	Gaps []struct {
		Kind    string `json:"kind"`
		Package string `json:"package"`
	} `json:"gaps"`
	Notices     []string `json:"notices"`
	Regressions int      `json:"regressions"`
	ExitCode    int      `json:"exit_code"`
}

func TestRunReportsAnAllocationRegression(t *testing.T) {
	t.Parallel()
	repo := testRepo(t, allocationRegression)

	code, stdout, stderr := runCLI(t, gateArgs(repo, "--fail-on-regression"))

	report := decodeReport(t, stdout, stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for a regression, got %d\nstderr:\n%s", code, stderr)
	}
	equals(t, 2, report.ExitCode)
	assert(t, report.Regressions > 0, "the report should count the regression")
	assert(t, report.Base.SHA != report.Head.SHA, "the two revisions should differ")

	// allocs/op is the assertion rather than ns/op on purpose: allocation counts
	// are exact and reproduce on any machine, while a timing threshold would
	// make this test flaky on a loaded runner — which is the same reason the
	// gate itself trusts them at a tighter tolerance.
	found := false
	for _, c := range report.Comparisons {
		if c.Unit != "allocs/op" || c.Verdict != "regressed" {
			continue
		}
		found = true
		assert(t, c.DeltaPercent != nil && *c.DeltaPercent > 0,
			"a regression in allocs/op should have a positive delta")
		assert(t, c.P != nil && *c.P < 0.05,
			"a regression should be statistically significant")
		equals(t, 5, c.SamplesBase)
		equals(t, 5, c.SamplesHead)
	}
	assert(t, found, "expected a regressed allocs/op row in:\n"+stdout)
}

// The same regression with no --fail-on-regression must still be reported and
// still exit 0. A gate that blocks before anyone has read what it says gets
// deleted rather than fixed.
func TestRunDoesNotBlockWithoutTheFlag(t *testing.T) {
	t.Parallel()
	repo := testRepo(t, allocationRegression)

	code, stdout, stderr := runCLI(t, gateArgs(repo))

	report := decodeReport(t, stdout, stderr)
	equals(t, 0, code)
	equals(t, 0, report.ExitCode)
	assert(t, report.Regressions > 0,
		"the regression should still be reported:\n"+stdout)
}

func TestRunPassesWhenNothingChanged(t *testing.T) {
	t.Parallel()
	// A second commit that touches only a comment: the benchmarks are
	// bit-identical, so nothing can regress.
	repo := testRepo(t, func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "hot.go")
		writeFile(t, path, readFile(t, path)+"\n// A comment, and nothing else.\n")
	})

	code, stdout, stderr := runCLI(t, gateArgs(repo, "--fail-on-regression"))

	report := decodeReport(t, stdout, stderr)
	equals(t, 0, code)
	equals(t, 0, report.Regressions)
	assert(t, len(report.Comparisons) > 0, "the benchmarks should still be compared")
}

func TestRunReportsBenchmarkCoverageGaps(t *testing.T) {
	t.Parallel()
	repo := testRepo(t, allocationRegression)

	code, stdout, stderr := runCLI(t, gateArgs(repo,
		"--require-benchmark-per-package", "--fail-on-gap"))

	report := decodeReport(t, stdout, stderr)
	// A regression outranks a gap, so the code is still 2 here; the gaps must
	// nonetheless be present in the report.
	assert(t, code == 2 || code == 3, fmt.Sprintf("expected a blocking exit, got %d", code))

	found := false
	for _, g := range report.Gaps {
		if g.Kind == "no-benchmark" && strings.HasSuffix(g.Package, "/sub") {
			found = true
		}
	}
	assert(t, found, "expected the benchmark-free package to be reported:\n"+stdout)
}

// The escape hatch. Without one, the first time the gate is wrong about
// something urgent, the whole job gets deleted instead of skipped.
func TestRunHonoursTheSkipMarker(t *testing.T) {
	t.Parallel()
	repo := testRepoWithMessage(t, allocationRegression,
		"tokenize: allocate per token [skip benchgate]")

	code, stdout, stderr := runCLI(t, gateArgs(repo, "--fail-on-regression"))

	report := decodeReport(t, stdout, stderr)
	equals(t, 0, code)
	equals(t, 0, len(report.Comparisons))
	assert(t, len(report.Notices) > 0, "a skipped run should say why")
	assert(t, strings.Contains(strings.Join(report.Notices, " "), "skip benchgate"),
		"the notice should name the marker: "+strings.Join(report.Notices, " "))
}

// A base ref that does not exist is the commonest CI misconfiguration, so the
// message has to name the fix rather than pass git's own words through.
func TestRunReportsAnUnresolvableBaseRef(t *testing.T) {
	t.Parallel()
	repo := testRepo(t, allocationRegression)

	code, _, stderr := runCLI(t,
		[]string{"benchgate", "check", "--dir", repo, "--base", "origin/nope"})

	equals(t, 1, code)
	assert(t, strings.Contains(stderr, "fetch-depth"),
		"the error should name the usual cause and fix: "+stderr)
}

func TestRunRejectsBadFlagValues(t *testing.T) {
	t.Parallel()
	repo := testRepo(t, allocationRegression)

	cases := map[string]struct{ args []string }{
		"alpha out of range":  {args: []string{"--alpha", "1.5"}},
		"negative tolerance":  {args: []string{"--tolerance", "-5"}},
		"coverage over 100":   {args: []string{"--min-bench-coverage", "150"}},
		"unknown format":      {args: []string{"--format", "yaml"}},
		"unexpected argument": {args: []string{"stray"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			args := append([]string{"benchgate", "check", "--dir", repo}, tc.args...)
			code, _, stderr := runCLI(t, args)
			equals(t, 1, code)
			assert(t, stderr != "", "a rejected invocation should explain itself")
		})
	}
}

// --help is not a failure, and neither is a bare invocation: both exit 0.
func TestRunTreatsHelpAsSuccess(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"no subcommand": {"benchgate"},
		"root help":     {"benchgate", "--help"},
		"check help":    {"benchgate", "check", "--help"},
		"compare help":  {"benchgate", "compare", "--help"},
		"gaps help":     {"benchgate", "gaps", "--help"},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, _, _ := runCLI(t, args)
			equals(t, 0, code)
		})
	}
}

func TestRunRejectsAnUnknownSubcommand(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, []string{"benchgate", "frobnicate"})
	equals(t, 1, code)
	assert(t, strings.Contains(stderr, "frobnicate"),
		"the error should name the unknown subcommand: "+stderr)
}

// `compare` is the offline path: two files, no git, no measurement.
func TestRunComparesTwoFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	header := "goos: linux\ngoarch: amd64\npkg: example.com/m\n"
	base, head := filepath.Join(dir, "base.txt"), filepath.Join(dir, "head.txt")
	writeFile(t, base, header+strings.Repeat("BenchmarkX-8\t100\t100.00 ns/op\n", 10))
	writeFile(t, head, header+strings.Repeat("BenchmarkX-8\t100\t200.00 ns/op\n", 10))

	code, stdout, stderr := runCLI(t, []string{
		"benchgate", "compare",
		"--format", "json", "--fail-on-regression",
		base, head,
	})

	report := decodeReport(t, stdout, stderr)
	equals(t, 2, code)
	equals(t, 1, report.Regressions)
	equals(t, base, report.Base.Ref)
	equals(t, "", report.Base.SHA) // a file has no commit behind it
	equals(t, 1, len(report.Comparisons))
	equals(t, "regressed", report.Comparisons[0].Verdict)
}

func TestRunCompareRejectsTheWrongArgumentCount(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"benchgate", "compare"},
		{"benchgate", "compare", "only-one.txt"},
		{"benchgate", "compare", "a.txt", "b.txt", "c.txt"},
	} {
		code, _, _ := runCLI(t, args)
		equals(t, 1, code)
	}
}

// Flag parsing stops at the first non-flag argument, so a flag written after
// the file names arrives as a third file. The message has to name that rather
// than report a confusing argument count.
func TestRunCompareExplainsFlagsAfterFileNames(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, []string{
		"benchgate", "compare", "base.txt", "head.txt", "--format", "json",
	})
	equals(t, 1, code)
	assert(t, strings.Contains(stderr, "flags must come before"),
		"the error should name the fix: "+stderr)
}

func TestRunCompareReportsAMissingFile(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, []string{
		"benchgate", "compare",
		filepath.Join(t.TempDir(), "absent.txt"),
		filepath.Join(t.TempDir(), "also-absent.txt"),
	})

	equals(t, 1, code)
	assert(t, strings.Contains(stderr, "absent.txt"),
		"the error should name the file: "+stderr)
}

// The markdown report also goes to a file, so a CI step can append it to a job
// summary while the log keeps the plain-text version.
func TestRunWritesASummaryFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	header := "goos: linux\ngoarch: amd64\npkg: example.com/m\n"
	base, head := filepath.Join(dir, "base.txt"), filepath.Join(dir, "head.txt")
	writeFile(t, base, header+strings.Repeat("BenchmarkX-8\t100\t100.00 ns/op\n", 10))
	writeFile(t, head, header+strings.Repeat("BenchmarkX-8\t100\t200.00 ns/op\n", 10))
	summary := filepath.Join(dir, "summary.md")
	// Pre-existing content, as a real step summary would have.
	writeFile(t, summary, "## An earlier step\n\n")

	code, stdout, _ := runCLI(t, []string{
		"benchgate", "compare",
		"--format", "text", "--summary-file", summary,
		base, head,
	})
	equals(t, 0, code)

	got := readFile(t, summary)
	assert(t, strings.HasPrefix(got, "## An earlier step"),
		"the summary file should be appended to, not truncated")
	assert(t, strings.Contains(got, "| Benchmark |"),
		"the summary file should hold the markdown table whatever --format says")
	assert(t, strings.Contains(stdout, "BENCHMARK"),
		"stdout should still hold the text table")
}

// ---------- helpers ----------

// gateArgs is the invocation the gate tests share: JSON on stdout, and a
// sampling plan chosen to be fast rather than precise.
//
// Five rounds, not fewer. The Mann-Whitney U-test cannot produce a p-value
// below 0.05 from fewer than four samples per side however far apart the
// measurements are, so a three-round run reports "unmeasurable" no matter what
// changed — which is correct behaviour and useless for asserting on. Five
// leaves margin. The assertions are all on allocation counts, which are exact,
// so this stays reliable on a loaded runner.
func gateArgs(repo string, extra ...string) []string {
	return append([]string{
		"benchgate", "check",
		"--dir", repo,
		"--base", "HEAD~1",
		"--rounds", "5",
		"--benchtime", "10x",
		"--warmup", "0",
		"--cooldown", "0",
		"--metric", "allocs/op",
		"--format", "json",
	}, extra...)
}

// allocationRegression rewrites the module so each token costs an allocation.
// Allocation counts are exact and machine-independent, which is what makes an
// end-to-end assertion on them reliable where a timing assertion would be flaky.
func allocationRegression(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "hot.go"), `package mod

import "strings"

// Tokens returns the whitespace-separated tokens of s.
func Tokens(s string) []string {
	var out []string
	for _, field := range strings.Fields(s) {
		// An allocation per token, where the base revision made one in total.
		out = append(out, strings.Clone(field))
	}
	return out
}
`)
}

// testRepo builds a throwaway git repository holding a benchmarkable module,
// commits it, applies change, and commits that too. The result has exactly the
// two commits a gate run compares.
func testRepo(t *testing.T, change func(*testing.T, string)) string {
	t.Helper()
	return testRepoWithMessage(t, change, "mod: change the implementation")
}

func testRepoWithMessage(t *testing.T, change func(*testing.T, string), message string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found on PATH")
	}
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "go.mod"), "module benchgate.test/mod\n\ngo 1.27.1\n")
	writeFile(t, filepath.Join(dir, "hot.go"), `package mod

import "strings"

// Tokens returns the whitespace-separated tokens of s.
func Tokens(s string) []string {
	return strings.Fields(s)
}
`)
	writeFile(t, filepath.Join(dir, "hot_test.go"), `package mod

import (
	"strings"
	"testing"
)

var corpus = strings.Repeat("the quick brown fox ", 32)

func BenchmarkTokens(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Tokens(corpus)
	}
}
`)
	// A second package with no benchmark at all, so the per-package gap check
	// has something true to find.
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatalf("creating the sub package: %s", err)
	}
	writeFile(t, filepath.Join(dir, "sub", "sub.go"),
		"package sub\n\n// Idle does nothing of consequence.\nfunc Idle() int { return 0 }\n")

	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "benchgate@example.test")
	runGit(t, dir, "config", "user.name", "benchgate test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "mod: add the module")

	change(t, dir)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", message)
	return dir
}

// runCLI drives run() exactly as main does, with injected I/O, and returns the
// exit code alongside what the command wrote. It is the one helper every test
// here shares, because the alternative is six lines of buffer plumbing per test
// (summary_rules §10: extract only what is truly universal).
//
// It takes no context: t.Context() is right for every test but the cancellation
// one, and a context parameter would have to come before t to satisfy revive's
// context-as-argument rule, which would in turn break thelper's requirement
// that a helper's first parameter be t. The one test that needs its own context
// calls run directly.
func runCLI(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = run(t.Context(), args, strings.NewReader(""), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func decodeReport(t *testing.T, stdout, stderr string) reportJSON {
	t.Helper()
	var report reportJSON
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decoding the report: %s\nstdout:\n%s\nstderr:\n%s",
			err, stdout, stderr)
	}
	return report
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// G204: the arguments are literals written in this file, not input.
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204, see above
	cmd.Dir = dir
	// An explicit environment keeps a developer's global git config out of the
	// test. t.Setenv is not an option: it disables t.Parallel.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=benchgate test",
		"GIT_AUTHOR_EMAIL=benchgate@example.test",
		"GIT_COMMITTER_NAME=benchgate test",
		"GIT_COMMITTER_EMAIL=benchgate@example.test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %s", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %s", path, err)
	}
	return string(b)
}

func assert(t *testing.T, condition bool, msg string) {
	t.Helper()
	if !condition {
		t.Fatal(msg)
	}
}

func equals(t *testing.T, exp, act any) {
	t.Helper()
	if exp != act {
		t.Fatalf("expected %v, got %v", exp, act)
	}
}

// Every test above passes t.Context(); this one asserts that a cancelled
// context is respected rather than ignored, so Ctrl-C during a long gate run
// stops it.
func TestRunHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	repo := testRepo(t, allocationRegression)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	equals(t, 1, run(ctx, gateArgs(repo), strings.NewReader(""), &stdout, &stderr))
}
