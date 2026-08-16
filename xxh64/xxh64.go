// Package xxh64 implements XXH64, the 64-bit hash from xxHash, with generated
// assembly kernels for amd64 and arm64.
//
// XXH64 is the older, scalar member of the xxHash family: four 64-bit lanes,
// each a multiply, a rotate and another multiply per eight bytes of input. It
// is what most Go code that says "xxhash" computes today. The output is
// bit-identical to the reference implementation, including seeds, and is
// checked against vectors taken from the C code.
//
// For the newer, faster XXH3, see the parent package.
//
// # Choosing an entry point
//
// [Sum64] and [Sum64String] hash a whole slice or string in one call;
// [Sum64Seed] and [Sum64SeedString] key the hash with a seed at no extra cost.
// For input arriving in pieces, [New] and [NewSeed] return a [Digest], which
// implements [hash.Hash64] and can be marshalled mid-stream.
//
// # Backends
//
// On amd64 and arm64 the whole hash of any input runs in one call into a
// generated assembly kernel; the arm64 kernel has the lane round in two
// forms, chosen by core (see [Backend]). Every other architecture, and a
// "purego" build, uses the portable implementation in this package, which is
// also what the kernels are checked against.
package xxh64

import (
	"math/bits"
	"unsafe"
)

// The XXH64 primes. Wire format: the hash changes if any of them does.
const (
	prime1 uint64 = 0x9E3779B185EBCA87
	prime2 uint64 = 0xC2B2AE3D27D4EB4F
	prime3 uint64 = 0x165667B19E3779F9
	prime4 uint64 = 0x85EBCA77C2B2AE63
	prime5 uint64 = 0x27D4EB2F165667C5
)

// primes is the table the arm64 kernels load their constants from: five
// 64-bit immediates would cost four instructions each in a kernel that has
// no constant pool, where three paired loads from here cost three. The x86
// kernels use immediates, which are one instruction there.
//
// The last two slots are not primes. Slot 5 is the arm64 lane-round form and
// slot 6 the amd64 prime form, each written once at package init (see the
// dispatch file for the architecture) and read by the kernel that wants it.
// They live here rather than in variables of their own because the kernel is
// already reading this cache line: on amd64 the prologue loads four primes
// out of it, so the form test costs no line the hash was not fetching
// anyway. A flag in a separate variable measured 4-8% of a 4..16-byte hash.
var primes = [7]uint64{prime1, prime2, prime3, prime4, prime5}

// The four entry points below are wrappers around one call into the kernel
// -- direct on amd64, through a variable chosen at startup on arm64 -- so
// that they inline into the caller and a hash costs one call in total. That
// is the whole reason the kernel takes short inputs too.

// Sum64 returns the XXH64 hash of b.
func Sum64(b []byte) uint64 {
	return sum64(unsafe.Pointer(unsafe.SliceData(b)), len(b), 0)
}

// Sum64String returns the XXH64 hash of s, without copying it.
func Sum64String(s string) uint64 {
	return sum64(unsafe.Pointer(unsafe.StringData(s)), len(s), 0)
}

// Sum64Seed returns the XXH64 hash of b under seed. A seed of zero gives the
// same result as [Sum64].
func Sum64Seed(b []byte, seed uint64) uint64 {
	return sum64(unsafe.Pointer(unsafe.SliceData(b)), len(b), seed)
}

// Sum64SeedString returns the XXH64 hash of s under seed, without copying it.
func Sum64SeedString(s string, seed uint64) uint64 {
	return sum64(unsafe.Pointer(unsafe.StringData(s)), len(s), seed)
}

// blockLen is the input consumed per round of the four lanes.
const blockLen = 32

// round is one lane's step: it absorbs a word, then stirs.
func round(acc, input uint64) uint64 {
	acc += input * prime2
	acc = bits.RotateLeft64(acc, 31)
	return acc * prime1
}

// mergeRound folds one finished lane into the hash.
func mergeRound(h, v uint64) uint64 {
	h ^= round(0, v)
	return h*prime1 + prime4
}

// initLanes is the lane state before the first block.
func initLanes(seed uint64) [4]uint64 {
	return [4]uint64{seed + prime1 + prime2, seed + prime2, seed, seed - prime1}
}

// mergeLanes converts the lane state, once every whole block is absorbed, into
// the running hash the tail then extends.
func mergeLanes(v *[4]uint64) uint64 {
	h := bits.RotateLeft64(v[0], 1) + bits.RotateLeft64(v[1], 7) +
		bits.RotateLeft64(v[2], 12) + bits.RotateLeft64(v[3], 18)
	h = mergeRound(h, v[0])
	h = mergeRound(h, v[1])
	h = mergeRound(h, v[2])
	h = mergeRound(h, v[3])
	return h
}

// blocksGeneric absorbs nb whole blocks from p into the lanes. This is the
// portable form of the blocks kernel, and the reference it is checked
// against.
func blocksGeneric(v *[4]uint64, p unsafe.Pointer, nb int) {
	v1, v2, v3, v4 := v[0], v[1], v[2], v[3]
	// Walked by offset rather than by advancing p: the pointer one past the
	// last block is not inside the input, and checkptr says so.
	for off := 0; off < nb*blockLen; off += blockLen {
		v1 = round(v1, rd64(p, off))
		v2 = round(v2, rd64(p, off+8))
		v3 = round(v3, rd64(p, off+16))
		v4 = round(v4, rd64(p, off+24))
	}
	v[0], v[1], v[2], v[3] = v1, v2, v3, v4
}

// sum64Generic is the whole hash in portable Go: what runs where there is
// no kernel, and what the kernels are checked against.
func sum64Generic(p unsafe.Pointer, n int, seed uint64) uint64 {
	if n < blockLen {
		return finalize(seed+prime5+uint64(n), p, n)
	}
	v := initLanes(seed)
	nb := n / blockLen
	blocksGeneric(&v, p, nb)
	h := mergeLanes(&v) + uint64(n)
	rem := n % blockLen
	if rem == 0 {
		return avalanche(h)
	}
	return finalize(h, add(p, nb*blockLen), rem)
}

// finalize absorbs the last n < 32 bytes at p into h, then avalanches. It is
// the tail of every hash: the reference's loops over eight-byte words, one
// four-byte word and single bytes, written out for the at most three, one
// and three of each that a tail can hold.
func finalize(h uint64, p unsafe.Pointer, n int) uint64 {
	// The reads are at offsets from p rather than through an advancing
	// pointer, which would end up one past the input.
	off := 0
	if n&16 != 0 {
		h ^= round(0, rd64(p, 0))
		h = bits.RotateLeft64(h, 27)*prime1 + prime4
		h ^= round(0, rd64(p, 8))
		h = bits.RotateLeft64(h, 27)*prime1 + prime4
		off = 16
	}
	if n&8 != 0 {
		h ^= round(0, rd64(p, off))
		h = bits.RotateLeft64(h, 27)*prime1 + prime4
		off += 8
	}
	if n&4 != 0 {
		h ^= uint64(rd32(p, off)) * prime1
		h = bits.RotateLeft64(h, 23)*prime2 + prime3
		off += 4
	}
	if n&2 != 0 {
		h ^= uint64(rdb(p, off)) * prime5
		h = bits.RotateLeft64(h, 11) * prime1
		h ^= uint64(rdb(p, off+1)) * prime5
		h = bits.RotateLeft64(h, 11) * prime1
		off += 2
	}
	if n&1 != 0 {
		h ^= uint64(rdb(p, off)) * prime5
		h = bits.RotateLeft64(h, 11) * prime1
	}
	return avalanche(h)
}

// avalanche is the final mix.
func avalanche(h uint64) uint64 {
	h ^= h >> 33
	h *= prime2
	h ^= h >> 29
	h *= prime3
	h ^= h >> 32
	return h
}

func add(p unsafe.Pointer, off int) unsafe.Pointer { return unsafe.Add(p, off) }

func rdb(p unsafe.Pointer, off int) byte { return *(*byte)(unsafe.Add(p, off)) }
