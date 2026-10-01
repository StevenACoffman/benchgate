package benchgate_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
)

// benchHeader is the configuration block `go test` prints before the records.
const benchHeader = "goos: linux\ngoarch: amd64\npkg: example.com/m\ncpu: Test CPU\n"

// Three assertion helpers, defined once per package, no assertion library
// (summary_rules §10).

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
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

// testPolicy fills in the defaults the caller left at zero and fails the test
// rather than returning an error.
func testPolicy(
	t *testing.T,
	sig benchgate.Significance,
	cov benchgate.Coverage,
	gates benchgate.Gates,
	metrics ...string,
) benchgate.Policy {
	t.Helper()
	if sig.Alpha == 0 {
		sig.Alpha = benchgate.DefaultAlpha
	}
	if sig.Confidence == 0 {
		sig.Confidence = benchgate.DefaultConfidence
	}
	p, err := benchgate.NewPolicy(sig, cov, gates, metrics)
	if err != nil {
		t.Fatalf("testPolicy: %s", err)
	}
	return p
}

// testSeries parses benchmark output, failing the test on a parse error.
func testSeries(t *testing.T, output string) benchgate.Series {
	t.Helper()
	s, err := benchgate.ParseResults(strings.NewReader(output), "test")
	if err != nil {
		t.Fatalf("testSeries: %s", err)
	}
	return s
}

// num renders a measurement the way `go test` does, so fixtures built by hand
// are byte-identical to real output.
func num(v float64) string { return fmt.Sprintf("%.2f", v) }

// benchOutput builds a `go test -bench` stream with one record per value, all
// for the same benchmark, measured in ns/op.
func benchOutput(name string, nsPerOp ...float64) string {
	var b strings.Builder
	b.WriteString(benchHeader)
	for _, ns := range nsPerOp {
		fmt.Fprintf(&b, "%s-8\t1000\t%.2f ns/op\n", name, ns)
	}
	return b.String()
}
