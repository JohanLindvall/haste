package rapidhash

import (
	"fmt"
	"testing"
)

// BenchmarkAPIVariants fills the gaps around the one-shot benchmarks. Calls
// stay direct so dispatch and wrapper inlining are included in the timing.
func BenchmarkAPIVariants(b *testing.B) {
	for _, n := range []int{0, 1, 3, 4, 8, 9, 16, 17, 32, 33, 64, 65, 96, 97, 112, 113, 128, 129, 224, 225, 240, 241, 256, 1024, 16384} {
		buf := testBuffer(n)
		str := string(buf)
		b.Run(fmt.Sprintf("String/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink = Sum64String(str)
			}
		})
		b.Run(fmt.Sprintf("SeedString/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink = Sum64SeedString(str, 42)
			}
		})
		b.Run(fmt.Sprintf("ZeroSeed/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink = Sum64Seed(buf, 0)
			}
		})
	}
}
