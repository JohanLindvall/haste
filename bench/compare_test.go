// Package bench compares xxhaste with the fastest XXH3 and XXH64
// implementations available for Go.
//
// zeebo/xxh3 is the reference point for XXH3: it is the established fast
// port, with hand-written AVX2 and SSE2 kernels on amd64 and pure Go
// elsewhere. cespare/xxhash is XXH64, included both because it is what most
// Go code actually calls today and because xxhaste/xxh64 computes the same
// hash.
package bench

import (
	"fmt"
	"testing"

	"github.com/JohanLindvall/xxhaste"
	"github.com/JohanLindvall/xxhaste/xxh64"
	cespare "github.com/cespare/xxhash/v2"
	"github.com/zeebo/xxh3"
)

var sizes = []int{4, 8, 16, 32, 64, 128, 240, 256, 512, 1024, 4096, 16384, 65536, 1 << 20}

func buffer(n int) []byte {
	b := make([]byte, n)
	g := uint64(2654435761)
	for i := range b {
		b[i] = byte(g >> 56)
		g *= 11400714785074694797
	}
	return b
}

var (
	sink64  uint64
	sink128 xxhaste.Uint128
)

// TestSameAsZeebo is a cross-implementation check: two independent ports of
// XXH3 must agree byte for byte, at every length where either changes path.
func TestSameAsZeebo(t *testing.T) {
	buf := buffer(1 << 16)
	for _, n := range []int{0, 1, 3, 4, 8, 9, 16, 17, 32, 64, 128, 129, 240, 241,
		256, 512, 1024, 1025, 4096, 65535, 65536} {
		in := buf[:n]
		if got, want := xxhaste.Sum64(in), xxh3.Hash(in); got != want {
			t.Errorf("len=%d: Sum64 %#016x != zeebo %#016x", n, got, want)
		}
		if got, want := xxhaste.Sum64Seed(in, 42), xxh3.HashSeed(in, 42); got != want {
			t.Errorf("len=%d: Sum64Seed %#016x != zeebo %#016x", n, got, want)
		}
		got, want := xxhaste.Sum128(in), xxh3.Hash128(in)
		if got.Lo != want.Lo || got.Hi != want.Hi {
			t.Errorf("len=%d: Sum128 {%#x,%#x} != zeebo {%#x,%#x}", n, got.Lo, got.Hi, want.Lo, want.Hi)
		}
		g2, w2 := xxhaste.Sum128Seed(in, 42), xxh3.Hash128Seed(in, 42)
		if g2.Lo != w2.Lo || g2.Hi != w2.Hi {
			t.Errorf("len=%d: Sum128Seed {%#x,%#x} != zeebo {%#x,%#x}", n, g2.Lo, g2.Hi, w2.Lo, w2.Hi)
		}
	}
}

// TestSameAsCespare is the same cross-implementation check for XXH64: the
// two ports must agree at every length where either changes path, seeded or
// not, one-shot or streamed.
func TestSameAsCespare(t *testing.T) {
	buf := buffer(1 << 16)
	for _, n := range []int{0, 1, 3, 4, 7, 8, 15, 16, 31, 32, 33, 63, 64, 65,
		100, 256, 1024, 1025, 4096, 65535, 65536} {
		in := buf[:n]
		if got, want := xxh64.Sum64(in), cespare.Sum64(in); got != want {
			t.Errorf("len=%d: xxh64.Sum64 %#016x != cespare %#016x", n, got, want)
		}
		c := cespare.NewWithSeed(42)
		c.Write(in)
		if got, want := xxh64.Sum64Seed(in, 42), c.Sum64(); got != want {
			t.Errorf("len=%d: xxh64.Sum64Seed %#016x != cespare %#016x", n, got, want)
		}
		d := xxh64.New()
		for off := 0; off < n; off += 7 {
			end := off + 7
			if end > n {
				end = n
			}
			d.Write(in[off:end])
		}
		if got, want := d.Sum64(), cespare.Sum64(in); got != want {
			t.Errorf("len=%d: xxh64.Digest %#016x != cespare %#016x", n, got, want)
		}
	}
}

func BenchmarkCompare64(b *testing.B) {
	for _, n := range sizes {
		buf := buffer(n)
		b.Run(fmt.Sprintf("%d/xxhaste", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxhaste.Sum64(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxh3.Hash(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/xxhaste-xxh64", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxh64.Sum64(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/cespare-xxh64", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = cespare.Sum64(buf)
			}
		})
	}
}

func BenchmarkCompare128(b *testing.B) {
	for _, n := range sizes {
		buf := buffer(n)
		b.Run(fmt.Sprintf("%d/xxhaste", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = xxhaste.Sum128(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				h := xxh3.Hash128(buf)
				sink128 = xxhaste.Uint128{Lo: h.Lo, Hi: h.Hi}
			}
		})
	}
}

func BenchmarkCompareSeed(b *testing.B) {
	for _, n := range []int{8, 64, 1024, 65536} {
		buf := buffer(n)
		b.Run(fmt.Sprintf("%d/xxhaste", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxhaste.Sum64Seed(buf, 42)
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxh3.HashSeed(buf, 42)
			}
		})
		b.Run(fmt.Sprintf("%d/xxhaste-xxh64", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxh64.Sum64Seed(buf, 42)
			}
		})
		// cespare/xxhash has no one-shot seeded form; a Digest reset with the
		// seed is its way, so that is what gets timed.
		b.Run(fmt.Sprintf("%d/cespare", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			d := cespare.NewWithSeed(42)
			for i := 0; i < b.N; i++ {
				d.ResetWithSeed(42)
				d.Write(buf)
				sink64 = d.Sum64()
			}
		})
	}
}

// BenchmarkCompareStreamChunk sweeps the write size: below a stripe the cost
// is all staging, above it the kernel takes over.
func BenchmarkCompareStreamChunk(b *testing.B) {
	const n = 1 << 20
	buf := buffer(n)
	for _, chunk := range []int{16, 64, 256, 1024, 4096} {
		b.Run(fmt.Sprintf("%d/xxhaste", chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := xxhaste.New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; off += chunk {
					d.Write(buf[off : off+chunk])
				}
				sink64 = d.Sum64()
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := xxh3.New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; off += chunk {
					d.Write(buf[off : off+chunk])
				}
				sink64 = d.Sum64()
			}
		})
		b.Run(fmt.Sprintf("%d/xxhaste-xxh64", chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := xxh64.New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; off += chunk {
					d.Write(buf[off : off+chunk])
				}
				sink64 = d.Sum64()
			}
		})
		b.Run(fmt.Sprintf("%d/cespare", chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := cespare.New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; off += chunk {
					d.Write(buf[off : off+chunk])
				}
				sink64 = d.Sum64()
			}
		})
	}
}

func BenchmarkCompareStream(b *testing.B) {
	const n = 1 << 20
	buf := buffer(n)
	b.Run("xxhaste", func(b *testing.B) {
		b.SetBytes(n)
		d := xxhaste.New()
		for i := 0; i < b.N; i++ {
			d.Reset()
			for off := 0; off < n; off += 4096 {
				d.Write(buf[off : off+4096])
			}
			sink64 = d.Sum64()
		}
	})
	b.Run("zeebo", func(b *testing.B) {
		b.SetBytes(n)
		d := xxh3.New()
		for i := 0; i < b.N; i++ {
			d.Reset()
			for off := 0; off < n; off += 4096 {
				d.Write(buf[off : off+4096])
			}
			sink64 = d.Sum64()
		}
	})
	b.Run("xxhaste-xxh64", func(b *testing.B) {
		b.SetBytes(n)
		d := xxh64.New()
		for i := 0; i < b.N; i++ {
			d.Reset()
			for off := 0; off < n; off += 4096 {
				d.Write(buf[off : off+4096])
			}
			sink64 = d.Sum64()
		}
	})
	b.Run("cespare-xxh64", func(b *testing.B) {
		b.SetBytes(n)
		d := cespare.New()
		for i := 0; i < b.N; i++ {
			d.Reset()
			for off := 0; off < n; off += 4096 {
				d.Write(buf[off : off+4096])
			}
			sink64 = d.Sum64()
		}
	})
}
