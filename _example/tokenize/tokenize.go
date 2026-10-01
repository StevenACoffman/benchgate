// Package tokenize splits text into whitespace-separated tokens.
//
// It exists to be benchmarked. Each exported function is deliberately a
// different shape of hot path — one that allocates nothing, one that allocates
// per token, one that allocates once — so a gate run over this package has
// something to say about ns/op, B/op, allocs/op and throughput rather than
// about a single number.
package tokenize

import (
	"strings"
	"unicode"
)

// Count returns the number of whitespace-separated tokens in s.
//
// It allocates nothing and reads s once, which makes it the cheapest thing in
// this package and the one whose timing is most sensitive to a careless change.
//
// Requires: nothing; the empty string is valid.
// Ensures:  Count(s) == len(Fields(s)) for every s.
func Count(s string) int {
	tokens := 0
	inToken := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			inToken = false
		case !inToken:
			inToken = true
			tokens++
		}
	}
	return tokens
}

// Fields splits s into its whitespace-separated tokens.
//
// Unlike Count this must allocate: a slice for the result, grown as it goes.
// The allocation count is the interesting measurement here, and it is a far
// less noisy signal on a shared CI runner than the wall time is.
//
// Ensures: no element of the result is empty, and none contains whitespace.
func Fields(s string) []string {
	// One allocation sized from the real token count beats several from
	// append's growth, and Count is cheap enough to pay for twice.
	out := make([]string, 0, Count(s))
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// Normalize lowercases s and collapses every run of whitespace to one space,
// with no leading or trailing space.
//
// Ensures: the result contains no consecutive spaces and no other whitespace;
// Count(Normalize(s)) == Count(s).
func Normalize(s string) string {
	var b strings.Builder
	// The result is never longer than the input, so one allocation does it.
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// LongestToken returns the longest token in s, and "" when there are none.
//
// There is deliberately no benchmark for this function. `benchgate gaps`
// reports it as unreached, which is the point: it is a worked example of the
// finding, and of why "all tests pass" says nothing about whether a change to
// this function would have been noticed.
func LongestToken(s string) string {
	longest := ""
	for _, token := range Fields(s) {
		if len(token) > len(longest) {
			longest = token
		}
	}
	return longest
}
