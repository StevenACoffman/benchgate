package benchgate_test

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
)

// Run `go test ./internal/benchgate/ -update`, read the diff, then commit the
// golden files (summary_rules §10).
var update = flag.Bool("update", false, "update the golden files")

// errWriteFailed is what failWriter returns, so a test can assert the error
// survived whatever wrapping the renderer applied.
var errWriteFailed = errors.New("write failed")

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

func TestRenderGolden(t *testing.T) {
	t.Parallel()

	report := testReport(t)
	cases := map[string]struct {
		format benchgate.Format
		golden string
	}{
		"text":     {format: benchgate.FormatText, golden: "report.txt"},
		"markdown": {format: benchgate.FormatMarkdown, golden: "report.md"},
		"json":     {format: benchgate.FormatJSON, golden: "report.json"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			ok(t, benchgate.Render(&buf, &report, tc.format))
			checkGolden(t, tc.golden, buf.Bytes())
		})
	}
}

func TestRenderEmptyReport(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{Tolerance: benchgate.DefaultTolerance},
		benchgate.Coverage{}, benchgate.Gates{})
	report := benchgate.NewReport(p)
	report.Base = benchgate.Revision{Ref: "main"}
	report.Head = benchgate.Revision{Ref: "HEAD"}
	report.Notices = []string{"no benchmarks found in ./..."}

	for _, format := range []benchgate.Format{
		benchgate.FormatText, benchgate.FormatMarkdown, benchgate.FormatJSON,
	} {
		var buf bytes.Buffer
		ok(t, benchgate.Render(&buf, &report, format))
		assert(t, buf.Len() > 0, "every format should render an empty report")
		assert(t, strings.Contains(buf.String(), "no benchmarks"),
			"the notice should survive into "+string(format))
	}
}

func TestParseFormat(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in      string
		want    benchgate.Format
		wantErr bool
	}{
		"text":            {in: "text", want: benchgate.FormatText},
		"markdown":        {in: "markdown", want: benchgate.FormatMarkdown},
		"json":            {in: "json", want: benchgate.FormatJSON},
		"mixed case":      {in: "JSON", want: benchgate.FormatJSON},
		"padded":          {in: "  text  ", want: benchgate.FormatText},
		"unknown":         {in: "yaml", wantErr: true},
		"empty":           {in: "", wantErr: true},
		"close but wrong": {in: "md", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := benchgate.ParseFormat(tc.in)
			if tc.wantErr {
				assert(t, err != nil, "an unknown format should be refused")
				equals(t, benchgate.EINVALID, benchgate.ErrorCode(err))
				return
			}
			ok(t, err)
			equals(t, tc.want, got)
		})
	}
}

func TestRevisionString(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		rev  benchgate.Revision
		want string
	}{
		"ref and sha are both shown, the sha abbreviated": {
			rev:  benchgate.Revision{Ref: "origin/main", SHA: "0123456789abcdef0123"},
			want: "origin/main (0123456789ab)",
		},
		"a file-sourced revision has only a ref": {
			rev:  benchgate.Revision{Ref: "base.txt"},
			want: "base.txt",
		},
		"a detached sha stands alone": {
			rev:  benchgate.Revision{SHA: "0123456789abcdef0123"},
			want: "0123456789ab",
		},
		"a short sha is not truncated": {
			rev:  benchgate.Revision{Ref: "HEAD", SHA: "abc123"},
			want: "HEAD (abc123)",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			equals(t, tc.want, tc.rev.String())
		})
	}
}

// testReport builds a report exercising every verdict and gap kind, so the
// golden files cover the whole rendering surface rather than the happy path.
func testReport(t *testing.T) benchgate.Report {
	t.Helper()

	p := testPolicy(t,
		benchgate.Significance{Tolerance: benchgate.DefaultTolerance},
		benchgate.Coverage{MinPercent: 80, RequirePerPackage: true, ReportUnreached: true},
		benchgate.Gates{Regression: true},
	)

	fast := []float64{100, 101, 99, 100, 102, 98, 101, 100, 99, 100}
	slow := []float64{130, 131, 129, 130, 132, 128, 131, 130, 129, 130}
	base := testSeries(t, benchOutput("BenchmarkSlower", fast...)+
		strings.TrimPrefix(benchOutput("BenchmarkGone", fast...), benchHeader))
	head := testSeries(t, benchOutput("BenchmarkSlower", slow...)+
		strings.TrimPrefix(benchOutput("BenchmarkNew", fast...), benchHeader))

	comparisons, err := benchgate.Compare(base, head, p)
	if err != nil {
		t.Fatalf("testReport: %s", err)
	}
	prof, err := benchgate.ParseFuncCoverage(strings.NewReader(coverFuncOutput))
	if err != nil {
		t.Fatalf("testReport: %s", err)
	}
	index, err := benchgate.ParseBenchmarkList(strings.NewReader(benchListOutput))
	if err != nil {
		t.Fatalf("testReport: %s", err)
	}

	report := benchgate.NewReport(p)
	report.Base = benchgate.Revision{
		Ref: "origin/main",
		SHA: "1111111111111111111111111111111111111111",
	}
	report.Head = benchgate.Revision{Ref: "HEAD", SHA: "2222222222222222222222222222222222222222"}
	report.BenchCommand = "go test -run ^$ -bench . -benchmem -count 1 ./..."
	report.Platform = base.Platform()
	report.CPU = base.CPU()
	report.Comparisons = comparisons
	report.Coverage = prof
	report.Gaps = benchgate.FindGaps(prof, index, p)
	return report
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	// go test sets the working directory to the package directory, so a
	// relative path is all that is needed (summary_rules §10).
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("checkGolden: writing %s: %s", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("checkGolden: reading %s (run with -update to create it): %s", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not match\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// The golden report above is one shape: every field populated, findings in
// every category. These cover the other shapes, because a renderer's absent-
// field and empty-collection branches are exactly the ones a single rich
// fixture never reaches.

func TestRenderSparseGolden(t *testing.T) {
	t.Parallel()

	// A gate run that resolved both revisions and then found nothing: no
	// platform, no CPU, no command, no comparisons, no gaps, no coverage.
	p := testPolicy(t, benchgate.Significance{Tolerance: benchgate.DefaultTolerance},
		benchgate.Coverage{}, benchgate.Gates{Regression: true})
	report := benchgate.NewReport(p)
	report.Base = benchgate.Revision{Ref: "origin/main"}
	report.Head = benchgate.Revision{Ref: "HEAD"}
	report.Notices = []string{
		"the base revision produced no benchmark results",
		"HEAD produced no benchmark results",
	}

	cases := map[string]struct {
		format benchgate.Format
		golden string
	}{
		"text":     {format: benchgate.FormatText, golden: "sparse.txt"},
		"markdown": {format: benchgate.FormatMarkdown, golden: "sparse.md"},
		"json":     {format: benchgate.FormatJSON, golden: "sparse.json"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			ok(t, benchgate.Render(&buf, &report, tc.format))
			checkGolden(t, tc.golden, buf.Bytes())
		})
	}
}

func TestRenderWarningsGolden(t *testing.T) {
	t.Parallel()

	report := testWarningReport(t)
	// The fixture has to actually carry warnings, or this asserts nothing.
	assert(t, len(report.Warnings()) > 1, "the warnings fixture should carry several warnings")

	cases := map[string]struct {
		format benchgate.Format
		golden string
	}{
		"text":     {format: benchgate.FormatText, golden: "warnings.txt"},
		"markdown": {format: benchgate.FormatMarkdown, golden: "warnings.md"},
		"json":     {format: benchgate.FormatJSON, golden: "warnings.json"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			ok(t, benchgate.Render(&buf, &report, tc.format))
			checkGolden(t, tc.golden, buf.Bytes())
		})
	}
}

// A coverage-only report has no revisions, so the header and the
// "no benchmarks compared" line must both change shape.
func TestRenderCoverageOnlyReport(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{},
		benchgate.Coverage{RequirePerPackage: true}, benchgate.Gates{Gap: true})
	report := benchgate.NewReport(p)
	report.BenchCommand = "go test ./... -run ^$ -bench ."
	report.Gaps = []benchgate.Gap{{
		Kind: benchgate.GapNoBenchmark, Package: "example.com/m/sub",
		Detail: "package declares no Benchmark function",
	}}

	equals(t, false, report.ComparesRevisions())

	var text, markdown bytes.Buffer
	ok(t, benchgate.RenderText(&text, &report))
	ok(t, benchgate.RenderMarkdown(&markdown, &report))

	assert(t, strings.Contains(text.String(), "benchmark coverage\n"),
		"a report with no revisions should not be headed as a gate:\n"+text.String())
	assert(t, !strings.Contains(text.String(), "benchmark gate:"),
		"a report with no revisions should not print a revision header")
	assert(t, !strings.Contains(text.String(), "no benchmarks compared"),
		"a coverage-only report should not announce a comparison it never attempted")
	assert(t, !strings.Contains(markdown.String(), "No benchmarks were compared."),
		"the same applies to the markdown rendering")
	assert(t, !strings.Contains(markdown.String(), "→"),
		"markdown should omit the revision arrow when there are no revisions")
}

// A report that compared revisions and found no benchmarks must say so, which
// is the opposite branch of the one above.
func TestRenderSaysWhenNothingWasCompared(t *testing.T) {
	t.Parallel()

	p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
	report := benchgate.NewReport(p)
	report.Base = benchgate.Revision{Ref: "main"}
	report.Head = benchgate.Revision{Ref: "HEAD"}

	var text, markdown bytes.Buffer
	ok(t, benchgate.RenderText(&text, &report))
	ok(t, benchgate.RenderMarkdown(&markdown, &report))
	assert(t, strings.Contains(text.String(), "no benchmarks compared"),
		"text should say nothing was compared:\n"+text.String())
	assert(t, strings.Contains(markdown.String(), "No benchmarks were compared."),
		"markdown should say nothing was compared:\n"+markdown.String())
}

// Every renderer writes to an io.Writer it does not control. A failing writer
// is the only way to reach their error paths, and a report that silently
// swallowed a write failure would look identical to one that succeeded.
func TestRenderPropagatesWriteErrors(t *testing.T) {
	t.Parallel()

	report := testReport(t)
	cases := map[string]func(io.Writer, *benchgate.Report) error{
		"text":     benchgate.RenderText,
		"markdown": benchgate.RenderMarkdown,
		"json":     benchgate.RenderJSON,
	}
	for name, render := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := render(failWriter{}, &report)
			assert(t, err != nil, "a failing writer should produce an error")
			assert(t, errors.Is(err, errWriteFailed),
				"the underlying write error should survive wrapping: "+err.Error())
		})
	}
}

func TestRenderRejectsAnUnknownFormat(t *testing.T) {
	t.Parallel()

	report := testReport(t)
	var buf bytes.Buffer
	err := benchgate.Render(&buf, &report, benchgate.Format("yaml"))
	assert(t, err != nil, "an unknown format should be refused")
	equals(t, benchgate.EINVALID, benchgate.ErrorCode(err))
	equals(t, 0, buf.Len())
}

// Each verdict gets its own marker in markdown. A renderer that mapped two
// verdicts to one marker would make a regression and an improvement look alike.
func TestRenderMarkdownDistinguishesEveryVerdict(t *testing.T) {
	t.Parallel()

	verdicts := []benchgate.Verdict{
		benchgate.VerdictRegressed, benchgate.VerdictImproved,
		benchgate.VerdictUnchanged, benchgate.VerdictUnmeasurable,
		benchgate.VerdictAdded, benchgate.VerdictRemoved,
		benchgate.Verdict("something-new"),
	}
	seen := make(map[string]benchgate.Verdict, len(verdicts))
	for _, v := range verdicts {
		p := testPolicy(t, benchgate.Significance{}, benchgate.Coverage{}, benchgate.Gates{})
		report := benchgate.NewReport(p)
		report.Base = benchgate.Revision{Ref: "main"}
		report.Head = benchgate.Revision{Ref: "HEAD"}
		report.Comparisons = []benchgate.Comparison{{
			Key: benchgate.Key{Name: "BenchmarkX"}, Metric: "sec/op",
			Unit: "ns/op", Verdict: v,
		}}

		var buf bytes.Buffer
		ok(t, benchgate.RenderMarkdown(&buf, &report))
		row := markdownRow(t, buf.String())
		if prev, dup := seen[row]; dup {
			t.Fatalf("verdicts %q and %q render identically as %q", prev, v, row)
		}
		seen[row] = v
		assert(t, strings.Contains(row, string(v)),
			"the marker for "+string(v)+" should name the verdict, got "+row)
	}
}

// markdownRow returns the verdict cell of the single table row in out.
func markdownRow(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "| `BenchmarkX`") {
			continue
		}
		cells := strings.Split(line, "|")
		return strings.TrimSpace(cells[len(cells)-2])
	}
	t.Fatalf("no benchmark row found in:\n%s", out)
	return ""
}

// testWarningReport builds a report whose comparisons carry warnings of every
// kind the gate produces: too few samples, an unstable measurement, and a unit
// with no known improvement direction.
func testWarningReport(t *testing.T) benchgate.Report {
	t.Helper()

	p := testPolicy(t, benchgate.Significance{Tolerance: benchgate.DefaultTolerance},
		benchgate.Coverage{}, benchgate.Gates{})

	// Three samples: below what the U-test needs at alpha 0.05, and below what
	// a confidence interval needs, so both warnings fire.
	base := testSeries(t, benchOutput("BenchmarkTiny", 100, 101, 99))
	head := testSeries(t, benchOutput("BenchmarkTiny", 130, 131, 129))
	// A wildly unstable pair with enough samples to be tested but not to be
	// trusted, which is what the instability warning exists to say.
	noisy := "BenchmarkNoisy-8\t1000\t%.2f ns/op\n"
	baseNoisy := benchHeader + fmtRows(noisy, 10, 500, 20, 900, 15, 700, 30, 1100)
	headNoisy := benchHeader + fmtRows(noisy, 12, 520, 25, 950, 18, 720, 35, 1200)
	base2, head2 := testSeries(t, baseNoisy), testSeries(t, headNoisy)

	comparisons, err := benchgate.Compare(base, head, p)
	if err != nil {
		t.Fatalf("testWarningReport: %s", err)
	}
	more, err := benchgate.Compare(base2, head2, p)
	if err != nil {
		t.Fatalf("testWarningReport: %s", err)
	}

	report := benchgate.NewReport(p)
	report.Base = benchgate.Revision{
		Ref: "origin/main",
		SHA: "3333333333333333333333333333333333333333",
	}
	report.Head = benchgate.Revision{Ref: "HEAD", SHA: "4444444444444444444444444444444444444444"}
	report.Platform = base.Platform()
	report.Comparisons = append(comparisons, more...)
	return report
}

func fmtRows(format string, values ...float64) string {
	var b strings.Builder
	for _, v := range values {
		fmt.Fprintf(&b, format, v)
	}
	return b.String()
}
