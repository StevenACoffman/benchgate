package benchgate_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
)

// Run `go test ./internal/benchgate/ -update`, read the diff, then commit the
// golden files (summary_rules §10).
var update = flag.Bool("update", false, "update the golden files")

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
