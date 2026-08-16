package xxh64

import (
	"fmt"
	"testing"
)

var sink uint64

// benchSizes covers the short path under a block, the one-block boundary,
// and the lane loop up to sizes that leave the cache.
var benchSizes = []int{4, 8, 16, 31, 32, 64, 128, 256, 1024, 4096, 16384, 65536, 1 << 20}

func BenchmarkSum64(b *testing.B) {
	for _, n := range benchSizes {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink = Sum64(buf)
			}
		})
	}
}

func BenchmarkSum64String(b *testing.B) {
	s := string(testBuffer(32))
	b.SetBytes(32)
	for i := 0; i < b.N; i++ {
		sink = Sum64String(s)
	}
}

func BenchmarkDigest(b *testing.B) {
	for _, n := range []int{64, 256, 1024, 16384, 1 << 20} {
		buf := testBuffer(n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			d := New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				d.Write(buf)
				sink = d.Sum64()
			}
		})
	}
}

// BenchmarkBackends runs the same sizes on every kernel this machine can
// execute, which is how the arm64 dispatch was decided.
func BenchmarkBackends(b *testing.B) {
	selected := Backend()
	defer setBackend(selected)
	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		b.Run(name, func(b *testing.B) {
			for _, n := range []int{32, 64, 256, 1024, 65536} {
				buf := testBuffer(n)
				b.Run(fmt.Sprint(n), func(b *testing.B) {
					b.SetBytes(int64(n))
					for i := 0; i < b.N; i++ {
						sink = Sum64(buf)
					}
				})
			}
		})
	}
}
