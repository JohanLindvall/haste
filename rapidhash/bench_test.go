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

// BenchmarkFixed times the entry points that take their input by value.
// They are the answer to the one cost a short hash cannot avoid otherwise:
// reaching the kernel. Compare each against BenchmarkSum64 at the same
// length -- four, eight and sixteen bytes -- which pays for the call.
func BenchmarkFixed(b *testing.B) {
	b.Run("Uint32", func(b *testing.B) {
		b.SetBytes(4)
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint32(0x89abcdef)
		}
	})
	b.Run("Uint64", func(b *testing.B) {
		b.SetBytes(8)
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint64(0x0123456789abcdef)
		}
	})
	b.Run("Uint128", func(b *testing.B) {
		b.SetBytes(16)
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint128(0x0123456789abcdef, 0xfedcba9876543210)
		}
	})
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
