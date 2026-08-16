package xxh64

import "math/bits"

// Fixed-size entry points, the same idea as the sibling packages' fixed.go.
//
// An eight-byte XXH64 is one tail step and an avalanche -- five multiplies
// and a handful of shifts -- and reaching the kernel costs a large share of
// that again: an empty call with the kernel's signature measures 1.28 ns on
// a Zen 4. These take their key by value, so the length is a compile-time
// constant, the block loop and the tail's bit tests fold away to nothing,
// and what is left folds into the caller.
//
// They are not a different hash. Each returns exactly what [Sum64] or
// [Sum64Seed] returns for the same bytes in little-endian order, which
// TestFixedMatchesSum64 checks over the whole 32-bit space and a large
// random sample of the wider ones.
//
// Unlike the rapidhash twins, these have seeded forms: XXH64 takes its seed
// as one add at the head of the hash, so a seed costs an instruction rather
// than a multiply and the seeded bodies still fit the inliner's budget.

// Sum64Uint32 returns the XXH64 hash of v's four little-endian bytes.
func Sum64Uint32(v uint32) uint64 { return Sum64Uint32Seed(v, 0) }

// Sum64Uint32Seed returns the XXH64 hash of v's four little-endian bytes
// under seed.
func Sum64Uint32Seed(v uint32, seed uint64) uint64 {
	h := seed + prime5 + 4
	h ^= uint64(v) * prime1
	h = bits.RotateLeft64(h, 23)*prime2 + prime3
	return avalanche(h)
}

// Sum64Uint64 returns the XXH64 hash of v's eight little-endian bytes.
func Sum64Uint64(v uint64) uint64 { return Sum64Uint64Seed(v, 0) }

// Sum64Uint64Seed returns the XXH64 hash of v's eight little-endian bytes
// under seed.
func Sum64Uint64Seed(v, seed uint64) uint64 {
	h := seed + prime5 + 8
	h ^= bits.RotateLeft64(v*prime2, 31) * prime1
	h = bits.RotateLeft64(h, 27)*prime1 + prime4
	return avalanche(h)
}

// Sum64Uint128 returns the XXH64 hash of the sixteen little-endian bytes of
// lo followed by hi.
//
// Its body is written out rather than calling the seeded form with a zero:
// this is the longest of the six, and the seed's add is what put it two
// nodes over the inliner's budget.
func Sum64Uint128(lo, hi uint64) uint64 {
	h := uint64(prime5 + 16)
	h ^= bits.RotateLeft64(lo*prime2, 31) * prime1
	h = bits.RotateLeft64(h, 27)*prime1 + prime4
	h ^= bits.RotateLeft64(hi*prime2, 31) * prime1
	h = bits.RotateLeft64(h, 27)*prime1 + prime4
	return avalanche(h)
}

// Sum64Uint128Seed returns the XXH64 hash of the sixteen little-endian bytes
// of lo followed by hi, under seed.
func Sum64Uint128Seed(lo, hi, seed uint64) uint64 {
	h := seed + prime5 + 16
	h ^= bits.RotateLeft64(lo*prime2, 31) * prime1
	h = bits.RotateLeft64(h, 27)*prime1 + prime4
	h ^= bits.RotateLeft64(hi*prime2, 31) * prime1
	h = bits.RotateLeft64(h, 27)*prime1 + prime4
	return avalanche(h)
}
