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

// BenchmarkDigestLocal includes construction when the digest is confined to
// its caller. BenchmarkNewSeed instead forces the returned pointer to escape.
func BenchmarkDigestLocal(b *testing.B) {
	for _, n := range []int{64, 256, 1024, 4096} {
		buf := testBuffer(n)
		secret := testSecret(193)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.Run("Default", func(b *testing.B) {
				b.SetBytes(int64(n))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					d := New()
					d.Write(buf)
					sink64 = d.Sum64()
				}
			})
			b.Run("Seed", func(b *testing.B) {
				b.SetBytes(int64(n))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					d := NewSeed(uint64(i))
					d.Write(buf)
					sink64 = d.Sum64()
				}
			})
			b.Run("Secret", func(b *testing.B) {
				b.SetBytes(int64(n))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					d := NewSecret(secret)
					d.Write(buf)
					sink64 = d.Sum64()
				}
			})
		})
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

// BenchmarkDigestSum measures non-destructive finalization separately from
// Write. Stripe-sized writes leave fully staged messages, the first drain,
// and an ongoing stream with whole stripes waiting to be finalized.
func BenchmarkDigestSum(b *testing.B) {
	for _, mode := range []struct {
		name string
		new  func() *Digest
	}{
		{"Default", New},
		{"Seed", func() *Digest { return NewSeed(42) }},
		{"Secret", func() *Digest { return NewSecret(testSecret(137)) }},
	} {
		b.Run(mode.name, func(b *testing.B) {
			for _, n := range []int{0, 16, 64, 128, 240, 241, 256, 576, 1024, 1025, 1536, 4096} {
				d := mode.new()
				writeInChunks(d, testBuffer(n), stripeLen)
				b.Run(fmt.Sprint(n), func(b *testing.B) {
					b.Run("64", func(b *testing.B) {
						b.ReportAllocs()
						for i := 0; i < b.N; i++ {
							sink64 = d.Sum64()
						}
					})
					b.Run("128", func(b *testing.B) {
						b.ReportAllocs()
						for i := 0; i < b.N; i++ {
							sink128 = d.Sum128()
						}
					})
				})
			}
		})
	}
}

// BenchmarkDigestSmallWrites covers odd sizes and the byte/string entry points
// with the same message. Neither conversion nor construction is timed.
func BenchmarkDigestSmallWrites(b *testing.B) {
	const n = 1 << 16
	buf := testBuffer(n)
	str := string(buf)
	for _, chunk := range []int{1, 3, 4, 8, 16, 31, 32, 63, 64, 65, 255} {
		b.Run(fmt.Sprint(chunk), func(b *testing.B) {
			b.Run("Bytes", func(b *testing.B) {
				d := New()
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
					sink64 = d.Sum64()
				}
			})
			b.Run("String", func(b *testing.B) {
				d := New()
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
						d.WriteString(str[off:end])
						off = end
					}
					sink64 = d.Sum64()
				}
			})
		})
	}
}
