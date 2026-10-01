package tokenize_test

import (
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/_example/tokenize"
)

// corpus is the benchmark input. It is built once, at package scope, because
// building it inside the loop would measure strings.Repeat rather than the
// function under test — and benchgate would then faithfully report regressions
// in the standard library.
var corpus = strings.Repeat("The Quick  Brown\tFox\nJumps over the lazy dog. ", 64)

func TestCount(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   string
		want int
	}{
		"empty":              {in: "", want: 0},
		"only whitespace":    {in: " \t\n ", want: 0},
		"one token":          {in: "hello", want: 1},
		"two tokens":         {in: "hello world", want: 2},
		"padded":             {in: "  hello  world  ", want: 2},
		"mixed whitespace":   {in: "a\tb\nc d", want: 4},
		"unicode is a token": {in: "héllo wörld", want: 2},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tokenize.Count(tc.in); got != tc.want {
				t.Errorf("Count(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// The contract Count documents, checked against the other implementation rather
// than against a hand-written number.
func TestCountAgreesWithFields(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", " ", "a", "a b", "  a  b  ", "a\tb\nc", corpus} {
		if got, want := tokenize.Count(in), len(tokenize.Fields(in)); got != want {
			t.Errorf("Count(%.20q) = %d, but Fields returned %d tokens", in, got, want)
		}
	}
}

func TestFields(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   string
		want []string
	}{
		"empty":    {in: "", want: nil},
		"one":      {in: "hello", want: []string{"hello"}},
		"two":      {in: "hello world", want: []string{"hello", "world"}},
		"padded":   {in: "  hello  world  ", want: []string{"hello", "world"}},
		"tabbed":   {in: "a\tb", want: []string{"a", "b"}},
		"trailing": {in: "a ", want: []string{"a"}},
		"leading":  {in: " a", want: []string{"a"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := tokenize.Fields(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("Fields(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Fields(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   string
		want string
	}{
		"empty":            {in: "", want: ""},
		"lowercased":       {in: "HeLLo", want: "hello"},
		"collapsed":        {in: "a    b", want: "a b"},
		"trimmed":          {in: "  a b  ", want: "a b"},
		"mixed whitespace": {in: "A\t\nB", want: "a b"},
		"only whitespace":  {in: "  \t", want: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tokenize.Normalize(tc.in); got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Count is the throughput case. b.SetBytes makes go report B/s alongside
// ns/op, and B/s is a unit where *higher* is better — which is exactly the
// case a gate that assumes "the number went up, so it got worse" gets
// backwards. benchgate reads the improvement direction per unit, so a faster
// Count is reported as an improvement here and not as a regression.
func BenchmarkCount(b *testing.B) {
	b.SetBytes(int64(len(corpus)))
	for b.Loop() {
		tokenize.Count(corpus)
	}
}

// Fields is the allocation case. B/op and allocs/op barely move with runner
// load, so on a shared CI machine they are the measurements a gate can trust
// at a tight tolerance while ns/op needs a loose one.
func BenchmarkFields(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		tokenize.Fields(corpus)
	}
}

func BenchmarkNormalize(b *testing.B) {
	b.ReportAllocs()
	b.SetBytes(int64(len(corpus)))
	for b.Loop() {
		tokenize.Normalize(corpus)
	}
}

// A sub-benchmark grid. Each case becomes its own row in the gate's report —
// "BenchmarkCountBySize/small", "BenchmarkCountBySize/large" — so a change that
// is fine on short input and quadratic on long input shows up as one regressed
// row rather than being averaged away.
func BenchmarkCountBySize(b *testing.B) {
	// A slice, not a map: Go randomises map iteration order, so a map here
	// would reorder the rows of every report and make two runs of the same
	// commit produce different-looking output.
	sizes := []struct {
		name   string
		repeat int
	}{
		{"small", 1},
		{"medium", 16},
		{"large", 256},
	}
	for _, size := range sizes {
		input := strings.Repeat("the quick brown fox ", size.repeat)
		b.Run(size.name, func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			for b.Loop() {
				tokenize.Count(input)
			}
		})
	}
}
