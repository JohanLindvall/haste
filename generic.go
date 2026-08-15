// The portable Go implementation of XXH3.
//
// It is always compiled, on every architecture, for two reasons: it is the
// fallback where no assembly backend exists, and it is the oracle the
// generated assembly backends are tested against. Every function here is a
// direct transcription of the reference implementation (xxHash v0.8.3), so the
// output is bit-identical by construction rather than by coincidence.
//
// Inputs are addressed through unsafe.Pointer rather than slices. The paths
// below are short enough that a bounds check per load is a measurable part of
// the cost, and every offset used here is already implied by the length switch
// in sum64 and sum128.

package xxhaste

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// The XXH32 and XXH64 primes. XXH3 uses both families: the 64-bit primes carry
// the main mixing, while the 32-bit ones seed accumulators and drive the
// scramble step's 64x32 multiply.
const (
	prime32_1 = 0x9E3779B1
	prime32_2 = 0x85EBCA77
	prime32_3 = 0xC2B2AE3D
	prime32_4 = 0x27D4EB2F
	prime32_5 = 0x165667B1

	prime64_1 = 0x9E3779B185EBCA87
	prime64_2 = 0xC2B2AE3D27D4EB4F
	prime64_3 = 0x165667B19E3779F9
	prime64_4 = 0x85EBCA77C2B2AE63
	prime64_5 = 0x27D4EB2F165667C5
)

// Structural constants of the long-input path and the mid-size ladders. These
// are wire format: changing any of them changes the hash.
const (
	// stripeLen is the number of input bytes absorbed per accumulator round,
	// and accNB the number of 64-bit accumulators, one per 8 bytes of stripe.
	stripeLen = 64
	accNB     = stripeLen / 8

	// secretConsumeRate is how far the secret advances per stripe. It is
	// deliberately less than stripeLen so consecutive stripes overlap in the
	// secret.
	secretConsumeRate = 8

	// secretSizeMin is the shortest secret accepted by the mid-size paths;
	// those paths index the secret relative to this, not to its real length.
	secretSizeMin = 136

	// midsizeMax is the largest input still handled by a mid-size ladder.
	// Above it, the SIMD accumulator path takes over.
	midsizeMax = 240

	// Offsets that de-align the second half of the 129..240 ladders from the
	// first, so a long input does not reuse the same secret bytes the same way.
	midsizeStartOffset = 3
	midsizeLastOffset  = 17

	// secretLastAccStart offsets the secret for the final stripe, and
	// secretMergeAccsStart offsets it for accumulator convergence; both are
	// intentionally unaligned relative to the per-stripe secret schedule.
	secretLastAccStart   = 7
	secretMergeAccsStart = 11

	// internalBufferSize is how much the streaming state stages before it
	// absorbs anything. It is a tuning parameter, not wire format: any value
	// at or above midsizeMax rounded up to a stripe gives the same hash.
	//
	// Its job is to amortize the cost of entering the kernel over more than
	// one write. Doubling it from the reference's 256 bytes was worth 17% on
	// 64-byte writes and 19% on 256-byte ones; going further traded small
	// writes against large ones and doubled the state again.
	internalBufferSize = 512
)

// Uint128 is a 128-bit hash value. Lo and Hi are the low and high halves as
// produced by XXH3; see Bytes for the canonical big-endian serialization.
type Uint128 struct {
	Lo, Hi uint64
}

// Bytes returns the canonical 16-byte big-endian encoding of h, matching the
// reference XXH128_canonicalFromHash: the high half first, then the low half.
func (h Uint128) Bytes() [16]byte {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], h.Hi)
	binary.BigEndian.PutUint64(b[8:16], h.Lo)
	return b
}

// rdb reads one byte; unlike rd32 and rd64 it needs no endian handling.
func rdb(p unsafe.Pointer, off uintptr) byte { return *(*byte)(unsafe.Add(p, off)) }

// add offsets a pointer. Every use below stays inside a buffer the caller
// already proved long enough.
func add(p unsafe.Pointer, off uintptr) unsafe.Pointer { return unsafe.Add(p, off) }

// initAcc is the starting accumulator state for the long-input path.
var initAcc = [accNB]uint64{
	prime32_3, prime64_1, prime64_2, prime64_3,
	prime64_4, prime32_2, prime64_5, prime32_1,
}

// xorshift64 is the shift-xor step shared by the avalanche finalizers.
func xorshift64(v uint64, shift uint) uint64 { return v ^ (v >> shift) }

// avalanche is XXH3's own finalizer, used wherever a value has already been
// well mixed and only needs its bits spread.
func avalanche(h uint64) uint64 {
	h = xorshift64(h, 37)
	h *= 0x165667919E3779F9
	return xorshift64(h, 32)
}

// avalanche64 is XXH64's stronger finalizer, used for the very short inputs
// whose keyed value has seen only one arithmetic step.
func avalanche64(h uint64) uint64 {
	h ^= h >> 33
	h *= prime64_2
	h ^= h >> 29
	h *= prime64_3
	h ^= h >> 32
	return h
}

// rrmxmx finalizes 4..8 byte inputs, folding the length in so that inputs of
// different lengths cannot collide through the multiply alone.
func rrmxmx(h, length uint64) uint64 {
	h ^= bits.RotateLeft64(h, 49) ^ bits.RotateLeft64(h, 24)
	h *= 0x9FB21C651E98DF25
	h ^= (h >> 35) + length
	h *= 0x9FB21C651E98DF25
	return xorshift64(h, 28)
}

// mul128Fold64 multiplies to 128 bits and folds the halves together with xor,
// the primitive that gives XXH3 its diffusion.
func mul128Fold64(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return lo ^ hi
}

// mix16BNS is mix16B for the unseeded case, which is most hashing: without a
// seed the two adds that key the secret are identity, and dropping them is
// worth 9-14% across the whole 17..128 ladder. The seeded twins below stay
// because a seed enters every mix, so it cannot be hoisted.
func mix16BNS(in, sec unsafe.Pointer) uint64 {
	return mul128Fold64(rd64(in, 0)^rd64(sec, 0), rd64(in, 8)^rd64(sec, 8))
}

// mix16B consumes 16 bytes of input against 16 bytes of secret.
func mix16B(in, sec unsafe.Pointer, seed uint64) uint64 {
	return mul128Fold64(
		rd64(in, 0)^(rd64(sec, 0)+seed),
		rd64(in, 8)^(rd64(sec, 8)-seed),
	)
}

// cross16 is the raw crossover term of a 16-byte chunk: its two words added.
func cross16(p unsafe.Pointer) uint64 { return rd64(p, 0) + rd64(p, 8) }

// mixHalf is one half of the 128-bit round. It folds a keyed 16-byte chunk
// into acc and crosses in the other chunk's raw term, which is what makes each
// half of the 128-bit hash depend on both chunks.
//
// The crossover is passed in rather than loaded here, and the two halves are
// separate functions rather than one. Both exist to keep this under the
// inliner's budget: a round that did all of it at once would be a call, and
// the mid-size ladders run up to eight of them.
func mixHalf(acc uint64, in, sec unsafe.Pointer, seed, cross uint64) uint64 {
	return (acc + mix16B(in, sec, seed)) ^ cross
}

// ---------------------------------------------------------------------------
// 64-bit, mid-size inputs
//
// The 0..16 cases live in sum64: they are short enough that a call would show
// up in the measurement. Everything from here on is called.
// ---------------------------------------------------------------------------

// len17to128_64 walks pairs of 16-byte chunks inward from both ends. The
// unrolled ladder means an input of any length in range touches a fixed,
// branch-predictable set of secret offsets.
//
// The single accumulator chain here is deliberate, and unlike the one in
// mergeAccs it does not want splitting. Its terms do not become ready at the
// same time -- each waits on its own pair of loads -- so a chain of adds
// absorbs them as they arrive, and a second partial sum only adds a final
// dependent add at the end. Measured on a Zen 4, splitting it cost 5% at 64
// bytes and 8% at 128. Seeding the chain with the length term rather than
// adding it at the end is worth another 4-7%, for the same reason: it gives
// the chain something to start on while the first loads are still in flight.
func len17to128_64(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, seed uint64) uint64 {
	acc := uint64(n) * prime64_1
	if n > 32 {
		if n > 64 {
			if n > 96 {
				acc += mix16B(add(in, 48), add(sec, 96), seed)
				acc += mix16B(add(in, n-64), add(sec, 112), seed)
			}
			acc += mix16B(add(in, 32), add(sec, 64), seed)
			acc += mix16B(add(in, n-48), add(sec, 80), seed)
		}
		acc += mix16B(add(in, 16), add(sec, 32), seed)
		acc += mix16B(add(in, n-32), add(sec, 48), seed)
	}
	acc += mix16B(in, sec, seed)
	acc += mix16B(add(in, n-16), add(sec, 16), seed)
	return avalanche(acc)
}

// len17to128_64NS is len17to128_64 with the seed arithmetic removed; see
// mix16BNS. The routing in sum64 keeps the two in lockstep: every unseeded
// reference vector runs through this one.
func len17to128_64NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer) uint64 {
	acc := uint64(n) * prime64_1
	if n > 32 {
		if n > 64 {
			if n > 96 {
				acc += mix16BNS(add(in, 48), add(sec, 96))
				acc += mix16BNS(add(in, n-64), add(sec, 112))
			}
			acc += mix16BNS(add(in, 32), add(sec, 64))
			acc += mix16BNS(add(in, n-48), add(sec, 80))
		}
		acc += mix16BNS(add(in, 16), add(sec, 32))
		acc += mix16BNS(add(in, n-32), add(sec, 48))
	}
	acc += mix16BNS(in, sec)
	acc += mix16BNS(add(in, n-16), add(sec, 16))
	return avalanche(acc)
}

// len129to240_64 runs a fixed 8-chunk prologue, avalanches, then a
// length-dependent tail. The mid-stream avalanche is what keeps this path from
// degenerating into a plain sum over many chunks.
//
// The prologue is written out rather than looped, and split across four
// partial sums. Both are safe: the chunk offsets are constants here, and the
// sums are added in the end, so regrouping them cannot change the result.
// What it buys is eight independent 128-bit multiplies in flight instead of a
// single chain of dependent adds.
func len129to240_64(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, seed uint64) uint64 {
	acc0 := uint64(n)*prime64_1 + mix16B(in, sec, seed)
	acc1 := mix16B(add(in, 16), add(sec, 16), seed)
	acc2 := mix16B(add(in, 32), add(sec, 32), seed)
	acc3 := mix16B(add(in, 48), add(sec, 48), seed)
	acc0 += mix16B(add(in, 64), add(sec, 64), seed)
	acc1 += mix16B(add(in, 80), add(sec, 80), seed)
	acc2 += mix16B(add(in, 96), add(sec, 96), seed)
	acc3 += mix16B(add(in, 112), add(sec, 112), seed)
	acc := avalanche((acc0 + acc1) + (acc2 + acc3))

	// The tail walks whatever whole 16-byte chunks are left, against a secret
	// offset by three bytes so it does not reuse the prologue's alignment.
	// Unrolled as in the seed-free twin; see the comment there.
	if n >= 144 {
		acc += mix16B(add(in, 128), add(sec, midsizeStartOffset), seed)
		if n >= 160 {
			acc += mix16B(add(in, 144), add(sec, midsizeStartOffset+16), seed)
			if n >= 176 {
				acc += mix16B(add(in, 160), add(sec, midsizeStartOffset+32), seed)
				if n >= 192 {
					acc += mix16B(add(in, 176), add(sec, midsizeStartOffset+48), seed)
					if n >= 208 {
						acc += mix16B(add(in, 192), add(sec, midsizeStartOffset+64), seed)
						if n >= 224 {
							acc += mix16B(add(in, 208), add(sec, midsizeStartOffset+80), seed)
							if n >= 240 {
								acc += mix16B(add(in, 224), add(sec, midsizeStartOffset+96), seed)
							}
						}
					}
				}
			}
		}
	}
	acc += mix16B(add(in, n-16), add(sec, secretSizeMin-midsizeLastOffset), seed)
	return avalanche(acc)
}

// len129to240_64NS is len129to240_64 without the seed; see mix16BNS.
func len129to240_64NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer) uint64 {
	acc0 := uint64(n)*prime64_1 + mix16BNS(in, sec)
	acc1 := mix16BNS(add(in, 16), add(sec, 16))
	acc2 := mix16BNS(add(in, 32), add(sec, 32))
	acc3 := mix16BNS(add(in, 48), add(sec, 48))
	acc0 += mix16BNS(add(in, 64), add(sec, 64))
	acc1 += mix16BNS(add(in, 80), add(sec, 80))
	acc2 += mix16BNS(add(in, 96), add(sec, 96))
	acc3 += mix16BNS(add(in, 112), add(sec, 112))
	acc := avalanche((acc0 + acc1) + (acc2 + acc3))

	// The tail is the reference's loop unrolled: mix i of the loop ran while
	// i < n/16, which is the chain of length tests below, in the same order
	// and adding into the same accumulator, so the hash cannot move. What the
	// loop paid per mix was its counter, its bound, and a multiply for the
	// offset; here every offset is an immediate, which is what lets the tail
	// issue as densely as the prologue above it.
	if n >= 144 {
		acc += mix16BNS(add(in, 128), add(sec, midsizeStartOffset))
		if n >= 160 {
			acc += mix16BNS(add(in, 144), add(sec, midsizeStartOffset+16))
			if n >= 176 {
				acc += mix16BNS(add(in, 160), add(sec, midsizeStartOffset+32))
				if n >= 192 {
					acc += mix16BNS(add(in, 176), add(sec, midsizeStartOffset+48))
					if n >= 208 {
						acc += mix16BNS(add(in, 192), add(sec, midsizeStartOffset+64))
						if n >= 224 {
							acc += mix16BNS(add(in, 208), add(sec, midsizeStartOffset+80))
							if n >= 240 {
								acc += mix16BNS(add(in, 224), add(sec, midsizeStartOffset+96))
							}
						}
					}
				}
			}
		}
	}
	acc += mix16BNS(add(in, n-16), add(sec, secretSizeMin-midsizeLastOffset))
	return avalanche(acc)
}

// ---------------------------------------------------------------------------
// 128-bit, short and mid-size inputs
// ---------------------------------------------------------------------------

// The seed-free twins of the three short 128-bit cases; see sum64NS for why
// they exist. Each is its seeded original with the seed terms deleted and
// nothing else changed.

// finalize128 converges the two accumulator halves into a 128-bit result. The
// high half is negated so that it cannot equal the low half for any input.
func finalize128(lo, hi uint64, length uintptr, seed uint64) Uint128 {
	return Uint128{
		Lo: avalanche(lo + hi),
		Hi: -avalanche(lo*prime64_1 + hi*prime64_4 + (uint64(length)-seed)*prime64_2),
	}
}

func len17to128_128(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, seed uint64) Uint128 {
	lo := uint64(n) * prime64_1
	hi := uint64(0)
	if n > 32 {
		if n > 64 {
			if n > 96 {
				a, b := add(in, 48), add(in, n-64)
				ca, cb := cross16(a), cross16(b)
				lo = mixHalf(lo, a, add(sec, 96), seed, cb)
				hi = mixHalf(hi, b, add(sec, 112), seed, ca)
			}
			a, b := add(in, 32), add(in, n-48)
			ca, cb := cross16(a), cross16(b)
			lo = mixHalf(lo, a, add(sec, 64), seed, cb)
			hi = mixHalf(hi, b, add(sec, 80), seed, ca)
		}
		a, b := add(in, 16), add(in, n-32)
		ca, cb := cross16(a), cross16(b)
		lo = mixHalf(lo, a, add(sec, 32), seed, cb)
		hi = mixHalf(hi, b, add(sec, 48), seed, ca)
	}
	a, b := in, add(in, n-16)
	ca, cb := cross16(a), cross16(b)
	lo = mixHalf(lo, a, sec, seed, cb)
	hi = mixHalf(hi, b, add(sec, 16), seed, ca)
	return finalize128(lo, hi, n, seed)
}

func len129to240_128(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, seed uint64) Uint128 {
	lo := uint64(n) * prime64_1
	hi := uint64(0)

	// Four fixed 32-byte rounds, written out for the same reason as in
	// len129to240_64. They are not split into partial sums: mix32B feeds each
	// half with the other's input, so the two halves are already interleaved.
	// Written out rather than looped: every offset is then a constant, and
	// mixHalf inlines, so the four rounds become straight-line code with
	// eight independent multiplies in flight.
	c0, c1 := cross16(in), cross16(add(in, 16))
	c2, c3 := cross16(add(in, 32)), cross16(add(in, 48))
	c4, c5 := cross16(add(in, 64)), cross16(add(in, 80))
	c6, c7 := cross16(add(in, 96)), cross16(add(in, 112))
	lo = mixHalf(lo, in, sec, seed, c1)
	hi = mixHalf(hi, add(in, 16), add(sec, 16), seed, c0)
	lo = mixHalf(lo, add(in, 32), add(sec, 32), seed, c3)
	hi = mixHalf(hi, add(in, 48), add(sec, 48), seed, c2)
	lo = mixHalf(lo, add(in, 64), add(sec, 64), seed, c5)
	hi = mixHalf(hi, add(in, 80), add(sec, 80), seed, c4)
	lo = mixHalf(lo, add(in, 96), add(sec, 96), seed, c7)
	hi = mixHalf(hi, add(in, 112), add(sec, 112), seed, c6)
	lo = avalanche(lo)
	hi = avalanche(hi)

	// The tail rounds unrolled; see len129to240_64NS for why the length
	// tests reproduce the loop's bound exactly.
	if n >= 160 {
		a, b := add(in, 128), add(in, 144)
		ca, cb := cross16(a), cross16(b)
		lo = mixHalf(lo, a, add(sec, midsizeStartOffset), seed, cb)
		hi = mixHalf(hi, b, add(sec, midsizeStartOffset+16), seed, ca)
		if n >= 192 {
			a, b := add(in, 160), add(in, 176)
			ca, cb := cross16(a), cross16(b)
			lo = mixHalf(lo, a, add(sec, midsizeStartOffset+32), seed, cb)
			hi = mixHalf(hi, b, add(sec, midsizeStartOffset+48), seed, ca)
			if n >= 224 {
				a, b := add(in, 192), add(in, 208)
				ca, cb := cross16(a), cross16(b)
				lo = mixHalf(lo, a, add(sec, midsizeStartOffset+64), seed, cb)
				hi = mixHalf(hi, b, add(sec, midsizeStartOffset+80), seed, ca)
			}
		}
	}
	// The tail deliberately takes its two chunks in reverse order.
	{
		a, b := add(in, n-16), add(in, n-32)
		ca, cb := cross16(a), cross16(b)
		s := add(sec, secretSizeMin-midsizeLastOffset-16)
		lo = mixHalf(lo, a, s, 0-seed, cb)
		hi = mixHalf(hi, b, add(s, 16), 0-seed, ca)
	}
	return finalize128(lo, hi, n, seed)
}

// len17to128_128NS is len17to128_128 without the seed.
func len17to128_128NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer) Uint128 {
	lo := uint64(n) * prime64_1
	hi := uint64(0)
	// Each round loads its four input words once and uses them twice -- keyed
	// in its own half's fold, raw as the other half's crossover. The mixHalf
	// form reloads them through cross16 and mix16B, a shape the inliner budget
	// forces on the seeded path; written out, the arithmetic is unchanged and
	// four loads per round disappear.
	if n > 32 {
		if n > 64 {
			if n > 96 {
				{
					i0, i1 := rd64(add(in, 48), 0), rd64(add(in, 48), 8)
					j0, j1 := rd64(add(in, n-64), 0), rd64(add(in, n-64), 8)
					lo = (lo + mul128Fold64(i0^rd64(add(sec, 96), 0), i1^rd64(add(sec, 96), 8))) ^ (j0 + j1)
					hi = (hi + mul128Fold64(j0^rd64(add(sec, 96+16), 0), j1^rd64(add(sec, 96+16), 8))) ^ (i0 + i1)
				}
			}
			{
				i0, i1 := rd64(add(in, 32), 0), rd64(add(in, 32), 8)
				j0, j1 := rd64(add(in, n-48), 0), rd64(add(in, n-48), 8)
				lo = (lo + mul128Fold64(i0^rd64(add(sec, 64), 0), i1^rd64(add(sec, 64), 8))) ^ (j0 + j1)
				hi = (hi + mul128Fold64(j0^rd64(add(sec, 64+16), 0), j1^rd64(add(sec, 64+16), 8))) ^ (i0 + i1)
			}
		}
		{
			i0, i1 := rd64(add(in, 16), 0), rd64(add(in, 16), 8)
			j0, j1 := rd64(add(in, n-32), 0), rd64(add(in, n-32), 8)
			lo = (lo + mul128Fold64(i0^rd64(add(sec, 32), 0), i1^rd64(add(sec, 32), 8))) ^ (j0 + j1)
			hi = (hi + mul128Fold64(j0^rd64(add(sec, 32+16), 0), j1^rd64(add(sec, 32+16), 8))) ^ (i0 + i1)
		}
	}
	{
		i0, i1 := rd64(in, 0), rd64(in, 8)
		j0, j1 := rd64(add(in, n-16), 0), rd64(add(in, n-16), 8)
		lo = (lo + mul128Fold64(i0^rd64(sec, 0), i1^rd64(sec, 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, 16), 0), j1^rd64(add(sec, 16), 8))) ^ (i0 + i1)
	}
	return finalize128(lo, hi, n, 0)
}

// len129to240_128NS is len129to240_128 without the seed.
func len129to240_128NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer) Uint128 {
	lo := uint64(n) * prime64_1
	hi := uint64(0)

	// The four fixed rounds and the tail are both written out; Go does not
	// unroll even a constant-count loop, and a round whose offsets are
	// immediates issues measurably denser than one that computes them. The
	// tail's length tests replicate the reference loop's bound in its order,
	// so the hash cannot move; see len129to240_64NS. Each round loads its
	// input words once, as in len17to128_128NS.
	{
		i0, i1 := rd64(in, 0), rd64(in, 8)
		j0, j1 := rd64(add(in, 16), 0), rd64(add(in, 16), 8)
		lo = (lo + mul128Fold64(i0^rd64(sec, 0), i1^rd64(sec, 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, 16), 0), j1^rd64(add(sec, 16), 8))) ^ (i0 + i1)
	}
	{
		i0, i1 := rd64(add(in, 32), 0), rd64(add(in, 32), 8)
		j0, j1 := rd64(add(in, 48), 0), rd64(add(in, 48), 8)
		lo = (lo + mul128Fold64(i0^rd64(add(sec, 32), 0), i1^rd64(add(sec, 32), 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, 48), 0), j1^rd64(add(sec, 48), 8))) ^ (i0 + i1)
	}
	{
		i0, i1 := rd64(add(in, 64), 0), rd64(add(in, 64), 8)
		j0, j1 := rd64(add(in, 80), 0), rd64(add(in, 80), 8)
		lo = (lo + mul128Fold64(i0^rd64(add(sec, 64), 0), i1^rd64(add(sec, 64), 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, 80), 0), j1^rd64(add(sec, 80), 8))) ^ (i0 + i1)
	}
	{
		i0, i1 := rd64(add(in, 96), 0), rd64(add(in, 96), 8)
		j0, j1 := rd64(add(in, 112), 0), rd64(add(in, 112), 8)
		lo = (lo + mul128Fold64(i0^rd64(add(sec, 96), 0), i1^rd64(add(sec, 96), 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, 112), 0), j1^rd64(add(sec, 112), 8))) ^ (i0 + i1)
	}
	lo = avalanche(lo)
	hi = avalanche(hi)

	if n >= 160 {
		i0, i1 := rd64(add(in, 128), 0), rd64(add(in, 128), 8)
		j0, j1 := rd64(add(in, 144), 0), rd64(add(in, 144), 8)
		lo = (lo + mul128Fold64(i0^rd64(add(sec, midsizeStartOffset), 0), i1^rd64(add(sec, midsizeStartOffset), 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, midsizeStartOffset+16), 0), j1^rd64(add(sec, midsizeStartOffset+16), 8))) ^ (i0 + i1)
		if n >= 192 {
			i0, i1 := rd64(add(in, 160), 0), rd64(add(in, 160), 8)
			j0, j1 := rd64(add(in, 176), 0), rd64(add(in, 176), 8)
			lo = (lo + mul128Fold64(i0^rd64(add(sec, midsizeStartOffset+32), 0), i1^rd64(add(sec, midsizeStartOffset+32), 8))) ^ (j0 + j1)
			hi = (hi + mul128Fold64(j0^rd64(add(sec, midsizeStartOffset+48), 0), j1^rd64(add(sec, midsizeStartOffset+48), 8))) ^ (i0 + i1)
			if n >= 224 {
				i0, i1 := rd64(add(in, 192), 0), rd64(add(in, 192), 8)
				j0, j1 := rd64(add(in, 208), 0), rd64(add(in, 208), 8)
				lo = (lo + mul128Fold64(i0^rd64(add(sec, midsizeStartOffset+64), 0), i1^rd64(add(sec, midsizeStartOffset+64), 8))) ^ (j0 + j1)
				hi = (hi + mul128Fold64(j0^rd64(add(sec, midsizeStartOffset+80), 0), j1^rd64(add(sec, midsizeStartOffset+80), 8))) ^ (i0 + i1)
			}
		}
	}
	// The tail deliberately takes its two chunks in reverse order.
	{
		i0, i1 := rd64(add(in, n-16), 0), rd64(add(in, n-16), 8)
		j0, j1 := rd64(add(in, n-32), 0), rd64(add(in, n-32), 8)
		lo = (lo + mul128Fold64(i0^rd64(add(sec, secretSizeMin-midsizeLastOffset-16), 0), i1^rd64(add(sec, secretSizeMin-midsizeLastOffset-16), 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, secretSizeMin-midsizeLastOffset), 0), j1^rd64(add(sec, secretSizeMin-midsizeLastOffset), 8))) ^ (i0 + i1)
	}
	return finalize128(lo, hi, n, 0)
}

// ---------------------------------------------------------------------------
// Long inputs: the accumulator path
// ---------------------------------------------------------------------------

// accumulate512Generic absorbs one 64-byte stripe into the accumulators.
//
// The lane swap (acc[i^1] += data) is what makes the accumulator order matter,
// and is why the SIMD backends must swap within each 128-bit half rather than
// across the whole vector.
func accumulate512Generic(acc *[accNB]uint64, in, sec unsafe.Pointer) {
	for i := uintptr(0); i < accNB; i++ {
		dataVal := rd64(in, 8*i)
		dataKey := dataVal ^ rd64(sec, 8*i)
		acc[i^1] += dataVal
		acc[i] += uint64(uint32(dataKey)) * uint64(dataKey>>32)
	}
}

// accumulateGeneric runs nbStripes consecutive stripes, advancing the secret by
// secretConsumeRate each time. The accumulators are lifted into locals so that
// the whole run keeps them in registers.
func accumulateGeneric(acc *[accNB]uint64, in, sec unsafe.Pointer, nbStripes int) {
	a0, a1, a2, a3 := acc[0], acc[1], acc[2], acc[3]
	a4, a5, a6, a7 := acc[4], acc[5], acc[6], acc[7]
	for ; nbStripes > 0; nbStripes-- {
		// Each lane adds its own keyed product and its neighbour's raw data:
		// acc[i] += mul32(data[i]^secret[i]), acc[i^1] += data[i].
		//
		// The stripe is walked one pair at a time, not loaded up front: the
		// lane swap only ever couples d[i] with d[i^1], so a pair is done with
		// its data as soon as it is absorbed. Loading all eight words first
		// needs 8 data + 8 accumulator registers and spills the accumulators
		// on amd64, which has ~14 to give; pair-wise, everything stays in
		// registers.
		d0, d1 := rd64(in, 0), rd64(in, 8)
		a0 += d1 + mul32(d0^rd64(sec, 0))
		a1 += d0 + mul32(d1^rd64(sec, 8))
		d2, d3 := rd64(in, 16), rd64(in, 24)
		a2 += d3 + mul32(d2^rd64(sec, 16))
		a3 += d2 + mul32(d3^rd64(sec, 24))
		d4, d5 := rd64(in, 32), rd64(in, 40)
		a4 += d5 + mul32(d4^rd64(sec, 32))
		a5 += d4 + mul32(d5^rd64(sec, 40))
		d6, d7 := rd64(in, 48), rd64(in, 56)
		a6 += d7 + mul32(d6^rd64(sec, 48))
		a7 += d6 + mul32(d7^rd64(sec, 56))
		in = add(in, stripeLen)
		sec = add(sec, secretConsumeRate)
	}
	acc[0], acc[1], acc[2], acc[3] = a0, a1, a2, a3
	acc[4], acc[5], acc[6], acc[7] = a4, a5, a6, a7
}

// mul32 is the accumulator's 32x32 half of the 64-bit lane: the low and high
// words of the keyed value multiplied together.
func mul32(dataKey uint64) uint64 {
	return uint64(uint32(dataKey)) * uint64(dataKey>>32)
}

// scrambleGeneric decorrelates the accumulators between blocks, so that a long
// input cannot be reduced to a simple sum over its stripes.
func scrambleGeneric(acc *[accNB]uint64, sec unsafe.Pointer) {
	for i := uintptr(0); i < accNB; i++ {
		acc[i] = (xorshift64(acc[i], 47) ^ rd64(sec, 8*i)) * prime32_1
	}
}

// hashLongGeneric consumes the whole input into acc: whole blocks each followed
// by a scramble, then the trailing stripes, then the final stripe taken from
// the very end of the input (which may overlap the previous one).
//
// It has the same signature as the assembly backends, and is what dispatch
// falls back to where none of them apply. secretLimit is len(secret)-stripeLen:
// both the scramble key and the final stripe's key are placed relative to it,
// and it is not necessarily a multiple of secretConsumeRate.
func hashLongGeneric(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) {
	nbStripesPerBlock := secretLimit / secretConsumeRate
	blockLen := stripeLen * nbStripesPerBlock

	// The trailing byte is held back so that an input landing exactly on a
	// stripe boundary still has a final stripe to absorb.
	rem := n - 1
	p := in
	for ; rem >= blockLen; rem -= blockLen {
		accumulateGeneric(acc, p, sec, nbStripesPerBlock)
		scrambleGeneric(acc, add(sec, uintptr(secretLimit)))
		p = add(p, uintptr(blockLen))
	}
	accumulateGeneric(acc, p, sec, rem/stripeLen)

	accumulate512Generic(acc, add(in, uintptr(n-stripeLen)),
		add(sec, uintptr(secretLimit-secretLastAccStart)))
}

// accumBlocksGeneric absorbs nbStripes stripes starting soFar stripes into the
// current block, scrambling at every boundary it crosses. It is the portable
// form of the streaming kernel; see the comment on emitAccumBlocks for why the
// block walk lives below the dispatch boundary rather than above it.
func accumBlocksGeneric(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int) {
	nbStripesPerBlock := secretLimit / secretConsumeRate
	s := add(sec, uintptr(soFar*secretConsumeRate))
	k := nbStripesPerBlock - soFar
	for nbStripes > 0 {
		cnt := k
		if cnt > nbStripes {
			cnt = nbStripes
		}
		accumulateGeneric(acc, in, s, cnt)
		in = add(in, uintptr(cnt*stripeLen))
		nbStripes -= cnt
		if cnt != k {
			return
		}
		scrambleGeneric(acc, add(sec, uintptr(secretLimit)))
		s, k = sec, nbStripesPerBlock
	}
}

// mix2Accs folds an accumulator pair through the 128-bit multiply.
func mix2Accs(acc *[accNB]uint64, i uintptr, sec unsafe.Pointer) uint64 {
	return mul128Fold64(acc[i]^rd64(sec, 0), acc[i+1]^rd64(sec, 8))
}

// mergeAccs converges the eight accumulators into one 64-bit value.
//
// The four folds are independent, so they are written out and summed as a
// tree rather than accumulated in a loop. This is the tail of every long
// hash, with nothing left to overlap it, so its dependency chain is its cost:
// worth 9% on a 256-byte hash and 4% on a kibibyte.
func mergeAccs(acc *[accNB]uint64, sec unsafe.Pointer, start uint64) uint64 {
	m0 := mix2Accs(acc, 0, sec)
	m1 := mix2Accs(acc, 2, add(sec, 16))
	m2 := mix2Accs(acc, 4, add(sec, 32))
	m3 := mix2Accs(acc, 6, add(sec, 48))
	return avalanche((start + m0) + (m1 + m2) + m3)
}

// deriveSecret builds the seeded secret used by long inputs. A seeded long hash
// is defined as the unseeded hash under this shifted secret, not as the default
// secret with a seed mixed in.
func deriveSecret(dst *[secretDefaultSize]byte, seed uint64) {
	src := unsafe.Pointer(&kSecret)
	for i := uintptr(0); i < secretDefaultSize/16; i++ {
		lo := rd64(src, 16*i) + seed
		hi := rd64(src, 16*i+8) - seed
		binary.LittleEndian.PutUint64(dst[16*i:], lo)
		binary.LittleEndian.PutUint64(dst[16*i+8:], hi)
	}
}
