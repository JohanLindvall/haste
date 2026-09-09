package xxh3

import (
	"fmt"
	"testing"
)

// BenchmarkSeededLong covers the secret setup cost for both output widths.
func BenchmarkSeededLong(b *testing.B) {
	for _, n := range []int{241, 256, 512, 1024, 4096, 16384} {
		buf := testBuffer(n)
		b.Run(fmt.Sprintf("64/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sink64 = Sum64Seed(buf, 42)
			}
		})
		b.Run(fmt.Sprintf("128/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sink128 = Sum128Seed(buf, 42)
			}
		})
	}
}

var digestSink *Digest

func BenchmarkNewSeed(b *testing.B) {
	for i := 0; i < b.N; i++ {
		digestSink = NewSeed(uint64(i))
	}
}

// BenchmarkAPIVariants fills the gaps around the one-shot benchmarks. Calls
// stay direct so dispatch and wrapper inlining are included in the timing.
func BenchmarkAPIVariants(b *testing.B) {
	for _, n := range []int{0, 1, 3, 4, 8, 9, 16, 17, 32, 33, 64, 65, 96, 97, 112, 113, 128, 129, 224, 225, 240, 241, 256, 1024, 16384} {
		buf := testBuffer(n)
		str := string(buf)
		sec := testSecret(193) // a secret whose block limit is not stripe-aligned
		b.Run(fmt.Sprintf("String/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = Sum64String(str)
			}
		})
		b.Run(fmt.Sprintf("SeedString/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = Sum64SeedString(str, 42)
			}
		})
		b.Run(fmt.Sprintf("ZeroSeed/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = Sum64Seed(buf, 0)
			}
		})
		b.Run(fmt.Sprintf("128String/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = Sum128String(str)
			}
		})
		b.Run(fmt.Sprintf("128SeedString/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = Sum128SeedString(str, 42)
			}
		})
		b.Run(fmt.Sprintf("128ZeroSeed/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = Sum128Seed(buf, 0)
			}
		})
		b.Run(fmt.Sprintf("Secret/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = Sum64Secret(buf, sec)
			}
		})
		b.Run(fmt.Sprintf("128Secret/%d", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = Sum128Secret(buf, sec)
			}
		})
	}
}
