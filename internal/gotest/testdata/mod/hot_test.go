package mod_test

import (
	"testing"

	"benchgate.test/mod"
)

func BenchmarkSum(b *testing.B) {
	for b.Loop() {
		mod.Sum(64)
	}
}
