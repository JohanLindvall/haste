package rapidhash

import (
	"encoding/binary"
	"math/bits"
)

// Fixed-size entry points.
//
// A rapidhash of eight bytes is a dozen instructions, and reaching the kernel
// costs about a third of that again: the entry points below inline into the
// caller instead, because with the length known there is nothing to branch on
// and every secret word the path touches is a constant.
//
// They are not a different hash. Each returns exactly what [Sum64] returns for
// the same bytes in little-endian order, which TestFixedMatchesSum64 checks
// over the whole input space it can reach.

// The secret words the 0..16 path keys with, and the prepared seed for each
// fixed length. Constants rather than loads: the seed is on the critical path
// to the multiply, and the array is a var the compiler will not fold.
// TestFixedConstants derives every one of them from [secret].
const (
	sec1 uint64 = 0x8bb84b93962eacc9 // secret[1]
	sec7 uint64 = 0xaaaaaaaaaaaaaaaa // secret[7]

	// secret[8] is the prologue's value for an unseeded call; these are it
	// with each fixed length folded in.
	seed4  uint64 = 0x422765567d8fbfd6 ^ 4
	seed8  uint64 = 0x422765567d8fbfd6 ^ 8
	seed16 uint64 = 0x422765567d8fbfd6 ^ 16
)

// fold is the last step of every rapidhash: the two words folded to 128 bits
// and back down, keyed by the length that remains.
func fold(a, b, n uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return mix(lo^sec7, hi^sec1^n)
}

// Sum64Uint32 returns the rapidhash of v's four little-endian bytes.
//
// The 4..16 path reads the first and last four bytes, which for a four-byte
// input are the same four, so v is both words.
func Sum64Uint32(v uint32) uint64 {
	return fold(uint64(v)^sec1, uint64(v)^seed4, 4)
}

// Sum64Uint64 returns the rapidhash of v's eight little-endian bytes.
func Sum64Uint64(v uint64) uint64 {
	return fold(v^sec1, v^seed8, 8)
}

// Sum64Uint128 returns the rapidhash of sixteen bytes: lo's eight
// little-endian bytes followed by hi's. For a UUID or any other fixed
// sixteen-byte array, read the halves out first, which costs two loads:
//
//	h := rapidhash.Sum64Uint128(
//		binary.LittleEndian.Uint64(id[:8]),
//		binary.LittleEndian.Uint64(id[8:]))
func Sum64Uint128(lo, hi uint64) uint64 {
	return fold(lo^sec1, hi^seed16, 16)
}

// Sum64Bytes8 returns the rapidhash of the eight bytes at b, for callers
// holding an array rather than an integer.
func Sum64Bytes8(b *[8]byte) uint64 {
	return Sum64Uint64(binary.LittleEndian.Uint64(b[:]))
}
