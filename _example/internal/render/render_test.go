package render_test

import (
	"testing"

	"github.com/StevenACoffman/benchgate/_example/internal/render"
)

// Tests but no benchmarks, on purpose: this is the package
// `--require-benchmark-per-package` reports. "The tests pass" and "a change
// here would be noticed" are different claims, and this package only supports
// the first one.
func TestSummary(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		tokens int
		want   string
	}{
		"none":     {tokens: 0, want: "no tokens"},
		"singular": {tokens: 1, want: "1 token"},
		"plural":   {tokens: 2, want: "2 tokens"},
		"many":     {tokens: 1024, want: "1024 tokens"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := render.Summary(tc.tokens); got != tc.want {
				t.Errorf("Summary(%d) = %q, want %q", tc.tokens, got, tc.want)
			}
		})
	}
}
