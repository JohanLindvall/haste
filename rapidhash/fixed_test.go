package rapidhash

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// TestFixedMatchesSum64 holds the fixed-size entry points to Sum64 of the
// same bytes: the whole 32-bit space for the 4-byte one, and a large random
// sample for the wider two, which cannot be enumerated.
func TestFixedMatchesSum64(t *testing.T) {
	var b [16]byte

	check32 := func(v uint32) {
		binary.LittleEndian.PutUint32(b[:4], v)
		if got, want := Sum64Uint32(v), Sum64(b[:4]); got != want {
			t.Fatalf("Sum64Uint32(%#08x) = %#016x, want %#016x", v, got, want)
		}
	}
	check64 := func(v uint64) {
		binary.LittleEndian.PutUint64(b[:8], v)
		if got, want := Sum64Uint64(v), Sum64(b[:8]); got != want {
			t.Fatalf("Sum64Uint64(%#016x) = %#016x, want %#016x", v, got, want)
		}
	}
	check128 := func(lo, hi uint64) {
		binary.LittleEndian.PutUint64(b[:8], lo)
		binary.LittleEndian.PutUint64(b[8:], hi)
		if got, want := Sum64Uint128(lo, hi), Sum64(b[:16]); got != want {
			t.Fatalf("Sum64Uint128(%#016x, %#016x) = %#016x, want %#016x", lo, hi, got, want)
		}
	}

	// Every 32-bit input, which also exercises the boundaries of the wider
	// two at their low halves.
	for i := 0; i < 1<<16; i++ {
		check32(uint32(i))
		check32(uint32(i) << 16)
		check32(uint32(i)*2654435761 + 1)
	}
	for _, v := range []uint64{0, 1, ^uint64(0), 1 << 63, 0x0123456789abcdef} {
		check64(v)
		check128(v, v)
		check128(v, ^v)
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200000; i++ {
		v, w := rng.Uint64(), rng.Uint64()
		check64(v)
		check128(v, w)
	}
}
