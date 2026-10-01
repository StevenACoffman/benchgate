// Package render formats a token count for display.
//
// This package declares no benchmark at all, on purpose. Running
// `benchgate gaps --require-benchmark-per-package` reports it, which is the
// worked example of that finding: nothing here is measured, so no measurement
// would ever notice it getting slower.
package render

import "strconv"

// Summary renders a token count as a short human-readable line.
func Summary(tokens int) string {
	switch tokens {
	case 0:
		return "no tokens"
	case 1:
		return "1 token"
	default:
		return strconv.Itoa(tokens) + " tokens"
	}
}
