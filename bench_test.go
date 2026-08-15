package xxhaste

import (
	"fmt"
	"testing"
)

// benchSizes covers the paths separately: the short ladders below 16 and 128
// bytes, the mid-size ladder up to 240, then the accumulator loop from one
// stripe's worth of trailing data up to sizes that no longer fit in cache.
var benchSizes = []int{4, 8, 16, 32, 64, 128, 240, 256, 512, 1024, 4096, 16384, 65536, 1 << 20}

func BenchmarkSum64(b *testing.B) {
	for _, n := range benchSizes {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = Sum64(buf)
			}
		})
	}
}

func BenchmarkSum128(b *testing.B) {
	for _, n := range benchSizes {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink128 = Sum128(buf)
			}
		})
	}
}

func BenchmarkSum64Seed(b *testing.B) {
	for _, n := range []int{8, 64, 240, 1024, 65536} {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink64 = Sum64Seed(buf, 0x9E3779B185EBCA87)
			}
		})
	}
}

func BenchmarkSum64String(b *testing.B) {
	s := string(testBuffer(32))
	b.SetBytes(32)
	for i := 0; i < b.N; i++ {
		sink64 = Sum64String(s)
	}
}

// BenchmarkDigest measures the streaming path, whose cost is dominated by
// staging input through the internal buffer rather than by the kernel.
func BenchmarkDigest(b *testing.B) {
	for _, n := range []int{64, 256, 1024, 16384, 1 << 20} {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			d := New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				d.Write(buf)
				sink64 = d.Sum64()
			}
		})
	}
}

// BenchmarkDigestChunked hashes a mebibyte written in pieces, the shape a file
// or network reader produces. The piece size is what decides how much of the
// cost is staging rather than hashing.
func BenchmarkDigestChunked(b *testing.B) {
	const n = 1 << 20
	buf := testBuffer(n)
	for _, chunk := range []int{16, 32, 64, 128, 256, 512, 1024, 4096, 65536} {
		b.Run(fmt.Sprint(chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := New()
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

// BenchmarkBackends runs the same sizes on every kernel this machine can
// execute, which is how the dispatch order is chosen.
func BenchmarkBackends(b *testing.B) {
	selected := Backend()
	defer setBackend(selected)
	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		b.Run(name, func(b *testing.B) {
			for _, n := range []int{256, 1024, 4096, 16384, 65536, 1 << 20} {
				buf := testBuffer(n)
				b.Run(fmt.Sprint(n), func(b *testing.B) {
					b.SetBytes(int64(n))
					for i := 0; i < b.N; i++ {
						sink64 = Sum64(buf)
					}
				})
			}
		})
	}
}

var sink128 Uint128
