package rapidhash

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// TestFixedConstants derives every constant in fixed.go from the secret it
// came from. They are written out so the fixed-size paths fold to a handful of
// instructions; this is what keeps them honest.
func TestFixedConstants(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uint64
	}{
		{"sec1", sec1, secret[1]},
		{"sec7", sec7, secret[7]},
		{"seed4", seed4, secret[8] ^ 4},
		{"seed8", seed8, secret[8] ^ 8},
		{"seed16", seed16, secret[8] ^ 16},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#016x, want %#016x", c.name, c.got, c.want)
		}
	}
}

// TestFixedMatchesSum64 is the whole contract of these entry points: the same
// bytes must give the same hash as the general path.
func TestFixedMatchesSum64(t *testing.T) {
	var b [16]byte

	check32 := func(v uint32) {
		t.Helper()
		binary.LittleEndian.PutUint32(b[:4], v)
		if got, want := Sum64Uint32(v), Sum64(b[:4]); got != want {
			t.Fatalf("Sum64Uint32(%#08x) = %#016x, want %#016x", v, got, want)
		}
	}
	check64 := func(v uint64) {
		t.Helper()
		binary.LittleEndian.PutUint64(b[:8], v)
		want := Sum64(b[:8])
		if got := Sum64Uint64(v); got != want {
			t.Fatalf("Sum64Uint64(%#016x) = %#016x, want %#016x", v, got, want)
		}
		var a8 [8]byte
		copy(a8[:], b[:8])
		if got := Sum64Bytes8(&a8); got != want {
			t.Fatalf("Sum64Bytes8(%#016x) = %#016x, want %#016x", v, got, want)
		}
	}
	check128 := func(lo, hi uint64) {
		t.Helper()
		binary.LittleEndian.PutUint64(b[:8], lo)
		binary.LittleEndian.PutUint64(b[8:], hi)
		if got, want := Sum64Uint128(lo, hi), Sum64(b[:]); got != want {
			t.Fatalf("Sum64Uint128(%#016x,%#016x) = %#016x, want %#016x", lo, hi, got, want)
		}
	}

	// Edges first: each path folds a length in, and zero, all-ones and the
	// halves-swapped cases are where a wrong fold shows up.
	for _, v := range []uint64{0, 1, ^uint64(0), 1 << 31, 1 << 32, 1<<32 - 1,
		0x0123456789abcdef, 0xfedcba9876543210} {
		check32(uint32(v))
		check32(uint32(v >> 32))
		check64(v)
		check128(v, ^v)
		check128(^v, v)
	}

	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 20000; i++ {
		v := rng.Uint64()
		check32(uint32(v))
		check64(v)
		check128(v, rng.Uint64())
	}

	// Every 32-bit input with a byte pattern worth distinguishing.
	for i := 0; i < 1<<16; i++ {
		check32(uint32(i))
		check32(uint32(i) << 16)
	}
}

func BenchmarkFixed(b *testing.B) {
	buf4, buf8, buf16 := make([]byte, 4), make([]byte, 8), make([]byte, 16)
	b.Run("Sum64Uint32", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint32(uint32(i))
		}
	})
	b.Run("Sum64/4", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64(buf4)
		}
	})
	b.Run("Sum64Uint64", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint64(uint64(i))
		}
	})
	b.Run("Sum64/8", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64(buf8)
		}
	})
	b.Run("Sum64Uint128", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64Uint128(uint64(i), 0x9e3779b1)
		}
	})
	b.Run("Sum64/16", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sink = Sum64(buf16)
		}
	})
}
