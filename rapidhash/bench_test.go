package rapidhash

import (
	"fmt"
	"testing"
)

var sink uint64

// benchSizes spans the three length classes and both block loops: the short
// path to 16, the 17..112 ladder, one and several 224-byte iterations, and
// sizes past cache.
var benchSizes = []int{4, 8, 16, 17, 32, 64, 100, 112, 113, 128, 224, 225,
	256, 512, 1024, 4096, 16384, 65536, 1 << 20}

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

func BenchmarkSum64String(b *testing.B) {
	for _, n := range []int{8, 16, 64, 256} {
		s := string(testBuffer(n))
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				sink = Sum64String(s)
			}
		})
	}
}

// BenchmarkBackends times every kernel this machine can run, not just the one
// dispatch picked, so a change can be judged against the alternative on the
// same hardware in the same binary.
func BenchmarkBackends(b *testing.B) {
	selected := Backend()
	defer setBackend(selected)

	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		for _, n := range benchSizes {
			buf := testBuffer(n)
			b.Run(fmt.Sprintf("%s/%d", name, n), func(b *testing.B) {
				b.SetBytes(int64(n))
				for i := 0; i < b.N; i++ {
					sink = Sum64(buf)
				}
			})
		}
	}
}
