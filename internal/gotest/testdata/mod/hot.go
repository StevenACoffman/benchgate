// Package mod is benchmark fodder: one function a benchmark reaches and one it
// does not, so a coverage report over it has something to say.
package mod

// Sum adds the integers below n. The benchmark exercises it.
func Sum(n int) int {
	total := 0
	for i := range n {
		total += i
	}
	return total
}

// Unreached is never called by any benchmark, so it is the gap the suite looks
// for in the coverage report.
func Unreached() string { return "nothing benchmarks this" }
