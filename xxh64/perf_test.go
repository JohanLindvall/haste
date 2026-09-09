package xxh64

import (
	"fmt"
	"testing"
)

// BenchmarkDigestChunked includes partial blocks and exact block boundaries.
func BenchmarkDigestChunked(b *testing.B) {
	const n = 1 << 16
	buf := testBuffer(n)
	for _, chunk := range []int{1, 4, 8, 16, 31, 32, 33, 64, 256, 1024, 4096} {
		b.Run(fmt.Sprint(chunk), func(b *testing.B) {
			d := NewSeed(42)
			b.SetBytes(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; {
					end := off + chunk
					if end > n {
						end = n
					}
					d.Write(buf[off:end])
					off = end
				}
				sink = d.Sum64()
			}
		})
	}
}

func BenchmarkSum64Seed(b *testing.B) {
	for _, n := range benchSizes {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink = Sum64Seed(buf, 42)
			}
		})
	}
}

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

func BenchmarkFixed(b *testing.B) {
	b.Run("Uint32", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint32(uint32(i))
		}
	})
	b.Run("Uint32Seed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint32Seed(uint32(i), 123)
		}
	})
	b.Run("Uint64", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint64(uint64(i))
		}
	})
	b.Run("Uint64Seed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint64Seed(uint64(i), 123)
		}
	})
	b.Run("Uint128", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint128(uint64(i), 42)
		}
	})
	b.Run("Uint128Seed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint128Seed(uint64(i), 42, 123)
		}
	})
}
