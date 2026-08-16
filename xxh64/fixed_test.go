package xxh64

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// TestFixedMatchesSum64 holds the fixed-size entry points to Sum64 and
// Sum64Seed of the same bytes. The 32-bit one is checked over a large part of
// its input space; the wider two cannot be enumerated, so they take the
// boundaries and a random sample.
func TestFixedMatchesSum64(t *testing.T) {
	var b [16]byte
	seeds := []uint64{0, 1, 42, 0x9E3779B185EBCA87, ^uint64(0)}

	check32 := func(v uint32, seed uint64) {
		binary.LittleEndian.PutUint32(b[:4], v)
		if got, want := Sum64Uint32Seed(v, seed), Sum64Seed(b[:4], seed); got != want {
			t.Fatalf("Sum64Uint32Seed(%#08x, %#x) = %#016x, want %#016x", v, seed, got, want)
		}
		if seed != 0 {
			return
		}
		if got, want := Sum64Uint32(v), Sum64(b[:4]); got != want {
			t.Fatalf("Sum64Uint32(%#08x) = %#016x, want %#016x", v, got, want)
		}
	}
	check64 := func(v, seed uint64) {
		binary.LittleEndian.PutUint64(b[:8], v)
		if got, want := Sum64Uint64Seed(v, seed), Sum64Seed(b[:8], seed); got != want {
			t.Fatalf("Sum64Uint64Seed(%#016x, %#x) = %#016x, want %#016x", v, seed, got, want)
		}
		if seed != 0 {
			return
		}
		if got, want := Sum64Uint64(v), Sum64(b[:8]); got != want {
			t.Fatalf("Sum64Uint64(%#016x) = %#016x, want %#016x", v, got, want)
		}
	}
	check128 := func(lo, hi, seed uint64) {
		binary.LittleEndian.PutUint64(b[:8], lo)
		binary.LittleEndian.PutUint64(b[8:], hi)
		if got, want := Sum64Uint128Seed(lo, hi, seed), Sum64Seed(b[:16], seed); got != want {
			t.Fatalf("Sum64Uint128Seed(%#016x, %#016x, %#x) = %#016x, want %#016x",
				lo, hi, seed, got, want)
		}
		if seed != 0 {
			return
		}
		if got, want := Sum64Uint128(lo, hi), Sum64(b[:16]); got != want {
			t.Fatalf("Sum64Uint128(%#016x, %#016x) = %#016x, want %#016x", lo, hi, got, want)
		}
	}

	// Byte patterns worth distinguishing, across every seed.
	for _, seed := range seeds {
		for i := 0; i < 1<<16; i++ {
			check32(uint32(i), seed)
			check32(uint32(i)<<16, seed)
		}
		for _, v := range []uint64{0, 1, ^uint64(0), 1 << 63, 0x0123456789abcdef} {
			check64(v, seed)
			check128(v, v, seed)
			check128(v, ^v, seed)
		}
	}

	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 100000; i++ {
		v, w := rng.Uint64(), rng.Uint64()
		seed := seeds[i%len(seeds)]
		check32(uint32(v), seed)
		check64(v, seed)
		check128(v, w, seed)
	}
}
