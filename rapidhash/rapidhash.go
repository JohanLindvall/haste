// Package rapidhash implements rapidhash, the 64-bit hash by Nicolas De
// Carli, with generated assembly kernels for amd64 and arm64.
//
// rapidhash is wyhash's successor: no vector unit at all, just the low and
// high halves of 64x64 multiplies folded together. That makes it a different
// shape of fast from XXH3 -- it wins where the multiplier is idle and the
// vector pipes are busy or absent -- and it is the default hash of several
// language runtimes for that reason.
//
// The output is bit-identical to the reference C implementation, including
// seeds, and is checked against vectors taken from that code.
//
// # Choosing an entry point
//
// [Sum64] and [Sum64String] hash a whole slice or string; [Sum64Seed] and
// [Sum64SeedString] key the hash with a seed at no extra cost. There is no
// streaming form: rapidhash reads the tail of its input before it has
// finished with the head, and needs the length before it starts, so it cannot
// be fed in pieces. Use the parent package's [github.com/JohanLindvall/haste/xxh3.Digest]
// when input arrives incrementally.
//
// # Backends
//
// On amd64 and arm64 the whole hash of any input runs in one call into a
// generated assembly kernel; [Backend] names the one in use. Every other
// architecture, and a "purego" build, uses the portable implementation in
// this package, which is also what the kernels are checked against.
package rapidhash

import (
	"math/bits"
	"unsafe"
)

// secret is rapidhash's default secret. Wire format: the hash changes if any
// word does. The last is not a random constant like the rest -- upstream
// gives it as 0xaaaa..., the alternating bit pattern -- and it keys only the
// final mix.
var secret = [8]uint64{
	0x2d358dccaa6c78a5,
	0x8bb84b93962eacc9,
	0x4b33a62ed433d4a3,
	0x4d5a2da51de1aa47,
	0xa0761d6478bd642f,
	0xe7037ed1a0b428db,
	0x90ed1765281c388c,
	0xaaaaaaaaaaaaaaaa,
}

// The four entry points are wrappers around one call into the kernel, so that
// they inline into the caller and a hash costs one call in total. That is the
// whole reason the kernel takes short inputs too.

// Sum64 returns the rapidhash of b.
func Sum64(b []byte) uint64 {
	return sum64(unsafe.Pointer(unsafe.SliceData(b)), len(b), 0)
}

// Sum64String returns the rapidhash of s, without copying it.
func Sum64String(s string) uint64 {
	return sum64(unsafe.Pointer(unsafe.StringData(s)), len(s), 0)
}

// Sum64Seed returns the rapidhash of b under seed. A seed of zero gives the
// same result as [Sum64].
func Sum64Seed(b []byte, seed uint64) uint64 {
	return sum64(unsafe.Pointer(unsafe.SliceData(b)), len(b), seed)
}

// Sum64SeedString returns the rapidhash of s under seed, without copying it.
func Sum64SeedString(s string, seed uint64) uint64 {
	return sum64(unsafe.Pointer(unsafe.StringData(s)), len(s), seed)
}

// ---------------------------------------------------------------------------
// The primitives
// ---------------------------------------------------------------------------

// mum multiplies to 128 bits and returns both halves. This is the whole of
// rapidhash's mixing: there is no rotate, no shift-xor, no addition chain.
//
// Upstream's RAPIDHASH_PROTECTED build xors the halves into the inputs
// instead of replacing them, which is a different hash; this is the default,
// unprotected form, which is what the published vectors are.
func mum(a, b uint64) (lo, hi uint64) {
	hi, lo = bits.Mul64(a, b)
	return lo, hi
}

// mix folds a 128-bit product down to 64 bits.
func mix(a, b uint64) uint64 {
	lo, hi := mum(a, b)
	return lo ^ hi
}

func add(p unsafe.Pointer, off int) unsafe.Pointer { return unsafe.Add(p, off) }

func rdb(p unsafe.Pointer, off int) byte { return *(*byte)(unsafe.Add(p, off)) }

// ---------------------------------------------------------------------------
// The portable implementation
// ---------------------------------------------------------------------------

// sum64Generic is a transcription of rapidhash_internal under the default
// secret. It is the oracle the assembly kernels are tested against, and the
// implementation every architecture without one uses.
//
// The structure is three length classes that do not share code: 0..16 reads
// its bytes directly, 17..112 walks a fixed unrolled ladder, and anything
// longer runs a seven-lane block loop first. All three converge on the same
// two words a and b, and the same final fold.
func sum64Generic(p unsafe.Pointer, n int, seed uint64) uint64 {
	seed ^= mix(seed^secret[2], secret[1])

	var a, b uint64
	i := n

	if n <= 16 {
		switch {
		case n >= 4:
			// The two reads overlap for lengths under 16, which is what lets
			// one path cover 4..16 without a branch per length. The length
			// goes into the seed here rather than into a or b.
			seed ^= uint64(n)
			if n >= 8 {
				a = rd64(p, 0)
				b = rd64(p, n-8)
			} else {
				a = uint64(rd32(p, 0))
				b = uint64(rd32(p, n-4))
			}
		case n > 0:
			// Three bytes at most, and the first is shifted 45 bits up so
			// that it cannot collide with the last.
			a = uint64(rdb(p, 0))<<45 | uint64(rdb(p, n-1))
			b = uint64(rdb(p, n>>1))
		}
	} else {
		if n > 112 {
			// Seven independent lanes, each keyed by its own secret word, so
			// the multiplies do not form one dependency chain. The loop takes
			// two rounds of seven per iteration.
			see1, see2, see3 := seed, seed, seed
			see4, see5, see6 := seed, seed, seed

			for i > 224 {
				seed = mix(rd64(p, 0)^secret[0], rd64(p, 8)^seed)
				see1 = mix(rd64(p, 16)^secret[1], rd64(p, 24)^see1)
				see2 = mix(rd64(p, 32)^secret[2], rd64(p, 40)^see2)
				see3 = mix(rd64(p, 48)^secret[3], rd64(p, 56)^see3)
				see4 = mix(rd64(p, 64)^secret[4], rd64(p, 72)^see4)
				see5 = mix(rd64(p, 80)^secret[5], rd64(p, 88)^see5)
				see6 = mix(rd64(p, 96)^secret[6], rd64(p, 104)^see6)
				seed = mix(rd64(p, 112)^secret[0], rd64(p, 120)^seed)
				see1 = mix(rd64(p, 128)^secret[1], rd64(p, 136)^see1)
				see2 = mix(rd64(p, 144)^secret[2], rd64(p, 152)^see2)
				see3 = mix(rd64(p, 160)^secret[3], rd64(p, 168)^see3)
				see4 = mix(rd64(p, 176)^secret[4], rd64(p, 184)^see4)
				see5 = mix(rd64(p, 192)^secret[5], rd64(p, 200)^see5)
				see6 = mix(rd64(p, 208)^secret[6], rd64(p, 216)^see6)
				p = add(p, 224)
				i -= 224
			}
			if i > 112 {
				seed = mix(rd64(p, 0)^secret[0], rd64(p, 8)^seed)
				see1 = mix(rd64(p, 16)^secret[1], rd64(p, 24)^see1)
				see2 = mix(rd64(p, 32)^secret[2], rd64(p, 40)^see2)
				see3 = mix(rd64(p, 48)^secret[3], rd64(p, 56)^see3)
				see4 = mix(rd64(p, 64)^secret[4], rd64(p, 72)^see4)
				see5 = mix(rd64(p, 80)^secret[5], rd64(p, 88)^see5)
				see6 = mix(rd64(p, 96)^secret[6], rd64(p, 104)^see6)
				p = add(p, 112)
				i -= 112
			}

			// The lanes converge in a fixed tree rather than a chain.
			seed ^= see1
			see2 ^= see3
			see4 ^= see5
			seed ^= see6
			see2 ^= see4
			seed ^= see2
		}

		// What is left is at most 112 bytes: a ladder of up to six rounds,
		// each conditional on the remaining length, keyed alternately by
		// secret[2] and secret[1].
		if i > 16 {
			seed = mix(rd64(p, 0)^secret[2], rd64(p, 8)^seed)
			if i > 32 {
				seed = mix(rd64(p, 16)^secret[2], rd64(p, 24)^seed)
				if i > 48 {
					seed = mix(rd64(p, 32)^secret[1], rd64(p, 40)^seed)
					if i > 64 {
						seed = mix(rd64(p, 48)^secret[1], rd64(p, 56)^seed)
						if i > 80 {
							seed = mix(rd64(p, 64)^secret[2], rd64(p, 72)^seed)
							if i > 96 {
								seed = mix(rd64(p, 80)^secret[1], rd64(p, 88)^seed)
							}
						}
					}
				}
			}
		}
		// The last 16 bytes, which may overlap what the ladder just read.
		a = rd64(p, i-16) ^ uint64(i)
		b = rd64(p, i-8)
	}

	a ^= secret[1]
	b ^= seed
	a, b = mum(a, b)
	// i, not n: the block loop consumed the difference, and the final fold is
	// keyed by what was left rather than by the whole length.
	return mix(a^secret[7], b^secret[1]^uint64(i))
}
