package rapidhash

import "math/bits"

// Fixed-size entry points, the same idea as the parent package's fixed.go.
//
// A four- or eight-byte rapidhash is two dependent multiplies and a handful
// of xors, and reaching the kernel costs more than that: an empty call with
// the kernel's signature measures 1.28 ns on a Zen 4 against 1.80 for the
// whole hash. These take their input by value, so the length is a constant,
// the length switch disappears, and each one folds into its caller.
//
// They are not a different hash. Each returns exactly what [Sum64] returns
// for the same bytes in little-endian order, which TestFixedMatchesSum64
// checks over the whole input space it can reach.
//
// Only the unseeded forms are here. A seed puts its own mix back at the head
// of the hash -- the thing sum64RapidNS exists to avoid -- and the result no
// longer fits the inliner's budget, which is what makes these worth having.

// Sum64Uint32 returns the rapidhash of v's four little-endian bytes.
//
// The 4..16 path reads a word from each end; at four bytes those are the same
// four bytes, read twice, which is what this folds away.
func Sum64Uint32(v uint32) uint64 {
	w := uint64(v)
	hi, lo := bits.Mul64(w^secret[1], w^secret[8]^4)
	hi2, lo2 := bits.Mul64(lo^secret[7], hi^secret[1]^4)
	return lo2 ^ hi2
}

// Sum64Uint64 returns the rapidhash of v's eight little-endian bytes.
func Sum64Uint64(v uint64) uint64 {
	hi, lo := bits.Mul64(v^secret[1], v^secret[8]^8)
	hi2, lo2 := bits.Mul64(lo^secret[7], hi^secret[1]^8)
	return lo2 ^ hi2
}

// Sum64Uint128 returns the rapidhash of the sixteen little-endian bytes of lo
// followed by hi.
func Sum64Uint128(lo, hi uint64) uint64 {
	h, l := bits.Mul64(lo^secret[1], hi^secret[8]^16)
	h2, l2 := bits.Mul64(l^secret[7], h^secret[1]^16)
	return l2 ^ h2
}
