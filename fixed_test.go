package xxhaste

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// TestBitflipConstants derives the constants in fixed.go from the secret they
// came from. They are written out so that the fixed-size paths fold to a few
// instructions; this is what keeps them honest.
func TestBitflipConstants(t *testing.T) {
	u := func(off int) uint64 { return binary.LittleEndian.Uint64(kSecret[off:]) }
	for _, c := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"bitflip4to8", bitflip4to8, u(8) ^ u(16)},
		{"bitflip9lo", bitflip9lo, u(24) ^ u(32)},
		{"bitflip9hi", bitflip9hi, u(40) ^ u(48)},
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

	seeds := []uint64{0, 1, 0x9E3779B185EBCA87, ^uint64(0)}
	check32 := func(v uint32) {
		t.Helper()
		binary.LittleEndian.PutUint32(b[:4], v)
		if got, want := Sum64Uint32(v), Sum64(b[:4]); got != want {
			t.Fatalf("Sum64Uint32(%#08x) = %#016x, want %#016x", v, got, want)
		}
		for _, s := range seeds {
			if got, want := Sum64Uint32Seed(v, s), Sum64Seed(b[:4], s); got != want {
				t.Fatalf("Sum64Uint32Seed(%#08x, %#x) = %#016x, want %#016x", v, s, got, want)
			}
		}
	}
	check64 := func(v uint64) {
		t.Helper()
		binary.LittleEndian.PutUint64(b[:8], v)
		want := Sum64(b[:8])
		if got := Sum64Uint64(v); got != want {
			t.Fatalf("Sum64Uint64(%#016x) = %#016x, want %#016x", v, got, want)
		}
		for _, s := range seeds {
			if got, want := Sum64Uint64Seed(v, s), Sum64Seed(b[:8], s); got != want {
				t.Fatalf("Sum64Uint64Seed(%#016x, %#x) = %#016x, want %#016x", v, s, got, want)
			}
		}
	}
	check128 := func(lo, hi uint64) {
		t.Helper()
		binary.LittleEndian.PutUint64(b[:8], lo)
		binary.LittleEndian.PutUint64(b[8:], hi)
		if got, want := Sum64Uint128(lo, hi), Sum64(b[:]); got != want {
			t.Fatalf("Sum64Uint128(%#016x,%#016x) = %#016x, want %#016x", lo, hi, got, want)
		}
		for _, s := range seeds {
			if got, want := Sum64Uint128Seed(lo, hi, s), Sum64Seed(b[:], s); got != want {
				t.Fatalf("Sum64Uint128Seed(%#016x,%#016x,%#x) = %#016x, want %#016x", lo, hi, s, got, want)
			}
		}
	}

	// Edges first: every path here folds a length in, and zero, all-ones and
	// the halves-swapped cases are where a wrong fold shows up.
	for _, v := range []uint64{0, 1, ^uint64(0), 1 << 31, 1 << 32, 1<<32 - 1,
		0x0123456789abcdef, 0xfedcba9876543210} {
		check32(uint32(v))
		check32(uint32(v >> 32))
		check64(v)
		check128(v, ^v)
		check128(^v, v)
	}

	rng := rand.New(rand.NewSource(2))
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
	buf4, buf8 := testBuffer(4), testBuffer(8)
	b.Run("Sum64Uint32", func(b *testing.B) {
		b.SetBytes(4)
		for i := 0; i < b.N; i++ {
			sink64 = Sum64Uint32(uint32(i))
		}
	})
	b.Run("Sum64/4", func(b *testing.B) {
		b.SetBytes(4)
		for i := 0; i < b.N; i++ {
			sink64 = Sum64(buf4)
		}
	})
	b.Run("Sum64Uint64", func(b *testing.B) {
		b.SetBytes(8)
		for i := 0; i < b.N; i++ {
			sink64 = Sum64Uint64(uint64(i))
		}
	})
	b.Run("Sum64/8", func(b *testing.B) {
		b.SetBytes(8)
		for i := 0; i < b.N; i++ {
			sink64 = Sum64(buf8)
		}
	})
	b.Run("Sum64Uint128", func(b *testing.B) {
		b.SetBytes(16)
		for i := 0; i < b.N; i++ {
			sink64 = Sum64Uint128(uint64(i), 0x9e3779b1)
		}
	})
	b.Run("Sum64/16", func(b *testing.B) {
		buf := testBuffer(16)
		b.SetBytes(16)
		for i := 0; i < b.N; i++ {
			buf[0] = byte(i)
			sink64 = Sum64(buf)
		}
	})
}
