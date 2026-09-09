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

// BenchmarkDigestChunkedLarge includes partial blocks as well as aligned writes,
// so completing a staged block is measured separately from the bulk loop.
func BenchmarkDigestChunkedLarge(b *testing.B) {
	const n = 1 << 20
	buf := testBuffer(n)
	for _, chunk := range []int{1, 7, 16, 31, 32, 33, 64, 65, 256, 1024, 65536} {
		b.Run(fmt.Sprint(chunk), func(b *testing.B) {
			b.SetBytes(n)
			d := New()
			for i := 0; i < b.N; i++ {
				d.Reset()
				for off := 0; off < n; off += chunk {
					end := off + chunk
					if end > n {
						end = n
					}
					d.Write(buf[off:end])
				}
				sink = d.Sum64()
			}
		})
	}
}

// BenchmarkDigestBackends also measures the alternative lane-round form,
// which is otherwise only benchmarked through the one-shot API.
func BenchmarkDigestBackends(b *testing.B) {
	selected := Backend()
	defer setBackend(selected)
	for _, name := range candidateBackends() {
		if setBackend(name) {
			b.Run(name, BenchmarkDigestChunkedLarge)
		}
	}
}

// BenchmarkBackends runs the same sizes on every kernel this machine can
// execute, which is how the arm64 dispatch was decided and how the amd64
// prime form is judged: the two forms are the same hash, and on amd64 the
// one not selected is reached through a jump the selected one does not pay,
// so the short lengths here measure the form and its dispatch together.
func BenchmarkBackends(b *testing.B) {
	selected := Backend()
	defer setBackend(selected)
	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		b.Run(name, func(b *testing.B) {
			for _, n := range []int{4, 8, 16, 32, 64, 256, 1024, 65536} {
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
