package xxhaste

import "math/bits"

// Fixed-size entry points.
//
// Hashing a short key costs about ten cycles of arithmetic, and reaching an
// out-of-line implementation costs nearly as much again: half of a Sum64 on
// eight bytes is the call. These take their input by value, so the length is
// known at compile time and there is nothing to switch on; each one folds into
// its caller.
//
// They are not a different hash. Each returns exactly what Sum64 returns for
// the same bytes in little-endian order, and TestFixedMatchesSum64 checks
// that over the whole input space it can reach.

// The 0..16 byte paths key their input with a pair of secret words. Under the
// default secret those are constants, which is what removes the loads — and
// with them the secret pointer, and with that the argument that would push
// these over the inliner's budget. TestBitflipConstants derives them from
// kSecret and fails if they ever disagree.
const (
	bitflip4to8 = 0xc73ab174c5ecd5a2 // kSecret[8:16] ^ kSecret[16:24]
	bitflip9lo  = 0x6782737bea4239b9 // kSecret[24:32] ^ kSecret[32:40]
	bitflip9hi  = 0xaf56bc3b0996523a // kSecret[40:48] ^ kSecret[48:56]
)

// Sum64Uint32 returns the 64-bit XXH3 hash of v's four little-endian bytes.
//
// The 4..8 byte path reads the first and last four bytes, which for a
// four-byte input are the same four, so v appears twice.
func Sum64Uint32(v uint32) uint64 {
	return rrmxmx(uint64(v)|uint64(v)<<32^bitflip4to8, 4)
}

// Sum64Uint64 returns the 64-bit XXH3 hash of v's eight little-endian bytes.
//
// The same path swaps the halves of the input before keying it, which for a
// whole 64-bit word is one rotate.
func Sum64Uint64(v uint64) uint64 {
	return rrmxmx(bits.RotateLeft64(v, 32)^bitflip4to8, 8)
}

// Sum64Uint32Seed returns the 64-bit XXH3 hash of v's four little-endian
// bytes, keyed by seed. A seed costs two instructions here, which is what
// makes it usable per hash table rather than per program.
func Sum64Uint32Seed(v uint32, seed uint64) uint64 {
	seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
	return rrmxmx(uint64(v)|uint64(v)<<32^(bitflip4to8-seed), 4)
}

// Sum64Uint64Seed returns the 64-bit XXH3 hash of v's eight little-endian
// bytes, keyed by seed.
func Sum64Uint64Seed(v, seed uint64) uint64 {
	seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
	return rrmxmx(bits.RotateLeft64(v, 32)^(bitflip4to8-seed), 8)
}

// Sum64Uint128Seed returns the 64-bit XXH3 hash of sixteen bytes, keyed by
// seed. See Sum64Uint128 for the byte order.
func Sum64Uint128Seed(lo, hi, seed uint64) uint64 {
	a := lo ^ (bitflip9lo + seed)
	b := hi ^ (bitflip9hi - seed)
	return avalanche(16 + bits.ReverseBytes64(a) + b + mul128Fold64(a, b))
}

// Sum64Uint128 returns the 64-bit XXH3 hash of sixteen bytes: lo's eight
// little-endian bytes followed by hi's. For a UUID or any other fixed
// sixteen-byte array, read the halves out first, which costs two loads:
//
//	h := xxhaste.Sum64Uint128(
//		binary.LittleEndian.Uint64(id[:8]),
//		binary.LittleEndian.Uint64(id[8:]))
//
// Taking the halves by value rather than the array by pointer is what keeps
// this inside the inliner's budget.
func Sum64Uint128(lo, hi uint64) uint64 {
	a := lo ^ bitflip9lo
	b := hi ^ bitflip9hi
	return avalanche(16 + bits.ReverseBytes64(a) + b + mul128Fold64(a, b))
}
