// Package bench compares haste with the fastest XXH3 and XXH64
// implementations available for Go.
//
// zeebo/xxh3 is the reference point for XXH3: it is the established fast
// port, with hand-written AVX2 and SSE2 kernels on amd64 and pure Go
// elsewhere. cespare/xxhash is XXH64, included both because it is what most
// Go code actually calls today and because haste/xxh64 computes the same
// hash. rapidhash is a third algorithm again -- no vector unit at all, just
// folded 64x64 multiplies -- included because it is what the comparison is
// for: knowing which shape of hash wins where.
//
// Two more ports join for each algorithm, per issue #7. bytedance/gopkg's
// xxhash3 is the only Go XXH3 besides zeebo's with hand-written vector paths.
// go.dw1.io/rapidhash and poiug07/rapidhash_go are Go rapidhash: the first
// assembly-free but current, the second explicitly not chasing speed.
//
// When a C compiler is present, the reference C implementations themselves
// join the comparison through cgo (see cref.go): xxHash pinned to v0.8.3, the
// revision the test vectors came from, and rapidhash as vendored beside it.
package bench

import (
	"fmt"
	"testing"

	"github.com/JohanLindvall/haste/rapidhash"
	"github.com/JohanLindvall/haste/xxh3"
	"github.com/JohanLindvall/haste/xxh64"
	bytedance "github.com/bytedance/gopkg/util/xxhash3"
	cespare "github.com/cespare/xxhash/v2"
	poiug07 "github.com/poiug07/rapidhash_go"
	zeebo "github.com/zeebo/xxh3"
	dw1 "go.dw1.io/rapidhash"
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
	sink128 xxh3.Uint128
)

// TestSameAsZeebo is a cross-implementation check: two independent ports of
// XXH3 must agree byte for byte, at every length where either changes path.
func TestSameAsZeebo(t *testing.T) {
	buf := buffer(1 << 16)
	for _, n := range []int{0, 1, 3, 4, 8, 9, 16, 17, 32, 64, 128, 129, 240, 241,
		256, 512, 1024, 1025, 4096, 65535, 65536} {
		in := buf[:n]
		if got, want := xxh3.Sum64(in), zeebo.Hash(in); got != want {
			t.Errorf("len=%d: Sum64 %#016x != zeebo %#016x", n, got, want)
		}
		if got, want := xxh3.Sum64Seed(in, 42), zeebo.HashSeed(in, 42); got != want {
			t.Errorf("len=%d: Sum64Seed %#016x != zeebo %#016x", n, got, want)
		}
		got, want := xxh3.Sum128(in), zeebo.Hash128(in)
		if got.Lo != want.Lo || got.Hi != want.Hi {
			t.Errorf("len=%d: Sum128 {%#x,%#x} != zeebo {%#x,%#x}", n, got.Lo, got.Hi, want.Lo, want.Hi)
		}
		g2, w2 := xxh3.Sum128Seed(in, 42), zeebo.Hash128Seed(in, 42)
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

// TestRapidhashSameAsC holds the Go rapidhash to the C implementation it was
// transcribed from, at every length where a path changes and a spread beyond.
// The package's own vectors came from that C code too; this asks it directly,
// in the same process, so a regenerated vector file cannot drift unnoticed.
func TestRapidhashSameAsC(t *testing.T) {
	if cRapid == nil {
		t.Skip("no C compiler; the reference is not linked in")
	}
	buf := buffer(1 << 16)
	lens := []int{}
	for n := 0; n <= 240; n++ {
		lens = append(lens, n)
	}
	lens = append(lens, 255, 256, 336, 448, 512, 1024, 4096, 65535, 65536)
	for _, n := range lens {
		in := buf[:n]
		if got, want := rapidhash.Sum64(in), cRapid(in); got != want {
			t.Fatalf("len=%d: Sum64 %#016x != C %#016x", n, got, want)
		}
		for _, seed := range []uint64{1, 42, 0x9e3779b185ebca87, ^uint64(0)} {
			if got, want := rapidhash.Sum64Seed(in, seed), cRapidSeed(in, seed); got != want {
				t.Fatalf("len=%d seed=%#x: Sum64Seed %#016x != C %#016x", n, seed, got, want)
			}
		}
	}
}

// TestSameAsBytedance is the second independent Go XXH3 held to this one.
// Its 128-bit result is [hi, lo] -- the reverse of the order the field names
// here imply -- which is worth a test rather than a comment, since getting it
// backwards produces two plausible-looking uint64s and no error.
func TestSameAsBytedance(t *testing.T) {
	buf := buffer(1 << 16)
	for _, n := range []int{0, 1, 3, 4, 8, 9, 16, 17, 32, 64, 128, 129, 240, 241,
		256, 512, 1024, 1025, 4096, 65535, 65536} {
		in := buf[:n]
		if got, want := bytedance.Hash(in), xxh3.Sum64(in); got != want {
			t.Errorf("len=%d: bytedance Hash %#016x != %#016x", n, got, want)
		}
		if got, want := bytedance.HashString(string(in)), xxh3.Sum64(in); got != want {
			t.Errorf("len=%d: bytedance HashString %#016x != %#016x", n, got, want)
		}
		h, c := bytedance.Hash128(in), xxh3.Sum128(in)
		if h[0] != c.Hi || h[1] != c.Lo {
			t.Errorf("len=%d: bytedance Hash128 [%#x,%#x] != {hi=%#x lo=%#x}",
				n, h[0], h[1], c.Hi, c.Lo)
		}
	}
}

// TestSameAsDW1 is the cross-check issue #7 was after: go.dw1.io/rapidhash
// implements the same version of the algorithm this package does, so the two
// must agree bit for bit, seeded and not. It is the only rapidhash peer that
// is a correctness check rather than only a benchmark one.
func TestSameAsDW1(t *testing.T) {
	buf := buffer(1 << 16)
	for _, n := range []int{0, 1, 3, 4, 8, 16, 17, 32, 64, 112, 113, 224, 225,
		240, 300, 336, 337, 512, 1024, 4096, 65535, 65536} {
		in := buf[:n]
		if got, want := dw1.Hash(in), rapidhash.Sum64(in); got != want {
			t.Errorf("len=%d: dw1 Hash %#016x != %#016x", n, got, want)
		}
		if got, want := dw1.HashString(string(in)), rapidhash.Sum64(in); got != want {
			t.Errorf("len=%d: dw1 HashString %#016x != %#016x", n, got, want)
		}
		for _, seed := range []uint64{1, 42, 0x9e3779b185ebca87, ^uint64(0)} {
			if got, want := dw1.HashWithSeed(in, seed), rapidhash.Sum64Seed(in, seed); got != want {
				t.Errorf("len=%d seed=%#x: dw1 HashWithSeed %#016x != %#016x", n, seed, got, want)
			}
		}
	}
}

// TestPoiug07DivergesAbove336 records where the third rapidhash port stops
// agreeing, and that everything below it does.
//
// It is the same algorithm version -- it matches at every length to 336 --
// and then differs at every length from 337 up. 337 is the first length that
// runs both the 224-byte loop and the 112-byte block after it, so that is
// where to look. This package matches the reference C at 335, 336 and 337,
// which ref/rapidgen.c emits vectors for precisely because the boundary is
// easy to get wrong.
//
// The rows in the benchmarks below are labelled accordingly: it is a fair
// speed peer up to 336 bytes and a different answer above it.
func TestPoiug07DivergesAbove336(t *testing.T) {
	buf := buffer(1024)
	for n := 0; n <= 336; n++ {
		if got, want := poiug07.Rapidhash(buf[:n]), rapidhash.Sum64(buf[:n]); got != want {
			t.Fatalf("len=%d: expected agreement below 337, got %#016x != %#016x", n, got, want)
		}
	}
	agreed := 0
	for n := 337; n <= 1024; n++ {
		if poiug07.Rapidhash(buf[:n]) == rapidhash.Sum64(buf[:n]) {
			agreed++
		}
	}
	if agreed != 0 {
		t.Errorf("poiug07 now agrees at %d lengths above 336; the divergence note "+
			"and the benchmark labels need revisiting", agreed)
	}
}

func BenchmarkCompare64(b *testing.B) {
	for _, n := range sizes {
		buf := buffer(n)
		b.Run(fmt.Sprintf("%d/haste-xxh3", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxh3.Sum64(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = zeebo.Hash(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/bytedance", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = bytedance.Hash(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/haste-xxh64", n), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("%d/haste-rapid", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = rapidhash.Sum64(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/dw1-rapid", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = dw1.Hash(buf)
			}
		})
		// Same algorithm to 336 bytes and a different answer above it; see
		// TestPoiug07DivergesAbove336. Read the larger sizes as a pure-Go
		// reference point, not as a like-for-like rapidhash.
		b.Run(fmt.Sprintf("%d/poiug07-rapid", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = poiug07.Rapidhash(buf)
			}
		})
		if cRapid != nil {
			b.Run(fmt.Sprintf("%d/c-rapidhash", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink64 = cRapid(buf)
				}
			})
		}
		if cXXH3 != nil {
			b.Run(fmt.Sprintf("%d/c", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink64 = cXXH3(buf)
				}
			})
			b.Run(fmt.Sprintf("%d/c-xxh64", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink64 = cXXH64(buf, 0)
				}
			})
		}
	}
}

func BenchmarkCompare128(b *testing.B) {
	for _, n := range sizes {
		buf := buffer(n)
		b.Run(fmt.Sprintf("%d/haste-xxh3", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = xxh3.Sum128(buf)
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				h := zeebo.Hash128(buf)
				sink128 = xxh3.Uint128{Lo: h.Lo, Hi: h.Hi}
			}
		})
		b.Run(fmt.Sprintf("%d/bytedance", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				h := bytedance.Hash128(buf)
				sink128 = xxh3.Uint128{Lo: h[1], Hi: h[0]}
			}
		})
		if cXXH3128 != nil {
			b.Run(fmt.Sprintf("%d/c", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					lo, hi := cXXH3128(buf)
					sink128 = xxh3.Uint128{Lo: lo, Hi: hi}
				}
			})
		}
	}
}

func BenchmarkCompareSeed(b *testing.B) {
	for _, n := range sizes {
		buf := buffer(n)
		b.Run(fmt.Sprintf("%d/haste-xxh3", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = xxh3.Sum64Seed(buf, 42)
			}
		})
		b.Run(fmt.Sprintf("%d/zeebo", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = zeebo.HashSeed(buf, 42)
			}
		})
		b.Run(fmt.Sprintf("%d/haste-xxh64", n), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("%d/haste-rapid", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = rapidhash.Sum64Seed(buf, 42)
			}
		})
		b.Run(fmt.Sprintf("%d/dw1-rapid", n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = dw1.HashWithSeed(buf, 42)
			}
		})
		if cRapidSeed != nil {
			b.Run(fmt.Sprintf("%d/c-rapidhash", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink64 = cRapidSeed(buf, 42)
				}
			})
		}
		if cXXH3Seed != nil {
			b.Run(fmt.Sprintf("%d/c", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink64 = cXXH3Seed(buf, 42)
				}
			})
			b.Run(fmt.Sprintf("%d/c-xxh64", n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink64 = cXXH64(buf, 42)
				}
			})
		}
	}
}

// BenchmarkCompareStreamChunk sweeps the write size: below a stripe the cost
// is all staging, above it the kernel takes over.
func BenchmarkCompareStreamChunk(b *testing.B) {
	const n = 1 << 20
	buf := buffer(n)
	for _, chunk := range []int{16, 64, 256, 1024, 4096, 16384, 65536} {
		b.Run(fmt.Sprintf("%d/haste-xxh3", chunk), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("%d/zeebo", chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := zeebo.New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; off += chunk {
					d.Write(buf[off : off+chunk])
				}
				sink64 = d.Sum64()
			}
		})
		b.Run(fmt.Sprintf("%d/haste-xxh64", chunk), func(b *testing.B) {
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
		if cXXH3Stream != nil {
			b.Run(fmt.Sprintf("%d/c", chunk), func(b *testing.B) {
				b.SetBytes(n)
				for i := 0; i < b.N; i++ {
					sink64 = cXXH3Stream(buf, chunk)
				}
			})
			b.Run(fmt.Sprintf("%d/c-xxh64", chunk), func(b *testing.B) {
				b.SetBytes(n)
				for i := 0; i < b.N; i++ {
					sink64 = cXXH64Stream(buf, chunk)
				}
			})
		}
	}
}

func BenchmarkCompareStream(b *testing.B) {
	const n = 1 << 20
	buf := buffer(n)
	b.Run("haste-xxh3", func(b *testing.B) {
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
	b.Run("zeebo", func(b *testing.B) {
		b.SetBytes(n)
		d := zeebo.New()
		for i := 0; i < b.N; i++ {
			d.Reset()
			for off := 0; off < n; off += 4096 {
				d.Write(buf[off : off+4096])
			}
			sink64 = d.Sum64()
		}
	})
	b.Run("haste-xxh64", func(b *testing.B) {
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
	if cXXH3Stream != nil {
		b.Run("c", func(b *testing.B) {
			b.SetBytes(n)
			for i := 0; i < b.N; i++ {
				sink64 = cXXH3Stream(buf, 4096)
			}
		})
		b.Run("c-xxh64", func(b *testing.B) {
			b.SetBytes(n)
			for i := 0; i < b.N; i++ {
				sink64 = cXXH64Stream(buf, 4096)
			}
		})
	}
}
