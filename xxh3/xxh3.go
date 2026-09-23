package xxh3

import (
	"math/bits"
	"unsafe"
)

// The public entry points are deliberately thin: each is small enough for the
// compiler to inline into its caller, so hashing a short key costs one call,
// into the matching seeded or unseeded core. The shortest inputs take about ten
// cycles of arithmetic, and a call level is a measurable share of that.

// Sum64 returns the 64-bit XXH3 hash of b.
func Sum64(b []byte) uint64 {
	return sum64NS(unsafe.Pointer(unsafe.SliceData(b)), uintptr(len(b)),
		unsafe.Pointer(&kSecret), secretDefaultSize)
}

// Sum64String returns the 64-bit XXH3 hash of s. It does not copy s.
func Sum64String(s string) uint64 {
	return sum64NS(unsafe.Pointer(unsafe.StringData(s)), uintptr(len(s)),
		unsafe.Pointer(&kSecret), secretDefaultSize)
}

// Sum64Seed returns the 64-bit XXH3 hash of b, keyed by seed.
//
// A seed does not change the cost for inputs up to 240 bytes. Beyond that XXH3
// keys the hash with a 192-byte secret derived from the seed. On amd64 the
// seed is applied as the secret is read, for up to a fifth more than an
// unseeded hash of the same length; past the length where that stops paying,
// and on other architectures from 241 bytes, the secret is derived per call,
// and a caller hashing many such inputs under one seed is better served by a
// Digest from NewSeed, which derives it once.
func Sum64Seed(b []byte, seed uint64) uint64 {
	return sum64Seeded(unsafe.Pointer(unsafe.SliceData(b)), uintptr(len(b)), seed)
}

// Sum64SeedString returns the 64-bit XXH3 hash of s, keyed by seed. It does not
// copy s.
func Sum64SeedString(s string, seed uint64) uint64 {
	return sum64Seeded(unsafe.Pointer(unsafe.StringData(s)), uintptr(len(s)), seed)
}

// Sum64Secret returns the 64-bit XXH3 hash of b under a custom secret, which is
// used verbatim rather than being derived from a seed.
//
// The secret must be at least MinSecretSize bytes and should be high-entropy:
// XXH3 keys every one of its paths from it, and a low-entropy secret weakens
// the hash. Sum64Secret panics if the secret is too short.
func Sum64Secret(b, secret []byte) uint64 {
	checkSecret(secret)
	return sum64NS(unsafe.Pointer(unsafe.SliceData(b)), uintptr(len(b)),
		unsafe.Pointer(unsafe.SliceData(secret)), len(secret))
}

// Sum128 returns the 128-bit XXH3 hash of b.
func Sum128(b []byte) Uint128 {
	return sum128NS(unsafe.Pointer(unsafe.SliceData(b)), uintptr(len(b)),
		unsafe.Pointer(&kSecret), secretDefaultSize)
}

// Sum128String returns the 128-bit XXH3 hash of s. It does not copy s.
func Sum128String(s string) Uint128 {
	return sum128NS(unsafe.Pointer(unsafe.StringData(s)), uintptr(len(s)),
		unsafe.Pointer(&kSecret), secretDefaultSize)
}

// Sum128Seed returns the 128-bit XXH3 hash of b, keyed by seed. The note on
// long inputs in Sum64Seed applies here too.
func Sum128Seed(b []byte, seed uint64) Uint128 {
	return sum128Seeded(unsafe.Pointer(unsafe.SliceData(b)), uintptr(len(b)), seed)
}

// Sum128SeedString returns the 128-bit XXH3 hash of s, keyed by seed. It does
// not copy s.
func Sum128SeedString(s string, seed uint64) Uint128 {
	return sum128Seeded(unsafe.Pointer(unsafe.StringData(s)), uintptr(len(s)), seed)
}

// Sum128Secret returns the 128-bit XXH3 hash of b under a custom secret. See
// Sum64Secret for the constraints on secret.
func Sum128Secret(b, secret []byte) Uint128 {
	checkSecret(secret)
	return sum128NS(unsafe.Pointer(unsafe.SliceData(b)), uintptr(len(b)),
		unsafe.Pointer(unsafe.SliceData(secret)), len(secret))
}

// MinSecretSize is the shortest custom secret accepted by this package, and
// matches XXH3_SECRET_SIZE_MIN in the reference implementation.
const MinSecretSize = secretSizeMin

func checkSecret(secret []byte) {
	if len(secret) < MinSecretSize {
		panic("xxh3: secret shorter than MinSecretSize")
	}
}

// ---------------------------------------------------------------------------
// 64-bit
// ---------------------------------------------------------------------------

// sum64NS hashes under a supplied secret without seed arithmetic. A seed
// enters each short case as one to four instructions -- the 4..8 case spends
// a byte-reverse, a shift, a xor and a subtract deriving its
// mix -- and on a core that is dispatch-saturated here, dead instructions are
// the whole cost. The short cases and 17..128-byte ladder live here to keep
// hashing within one call, without testing a seed. The seeded core below
// has the same shape and is held to the same reference vectors.
//
// The stack check is a measurable part of a short hash, so the cores are
// nosplit; the linker verifies their stack budgets at build time.
//
//go:nosplit
func sum64NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, secretLen int) uint64 {
	if n <= 16 {
		if n > 8 {
			inputLo := rd64(in, 0) ^ (rd64(sec, 24) ^ rd64(sec, 32))
			inputHi := rd64(in, n-8) ^ (rd64(sec, 40) ^ rd64(sec, 48))
			acc := uint64(n) + bits.ReverseBytes64(inputLo) + inputHi + mul128Fold64(inputLo, inputHi)
			return avalanche(acc)
		}
		if n >= 4 {
			bitflip := rd64(sec, 8) ^ rd64(sec, 16)
			return rrmxmx(uint64(rd32(in, n-4))+uint64(rd32(in, 0))<<32^bitflip, uint64(n))
		}
		if n > 0 {
			combined := uint32(rdb(in, 0))<<16 | uint32(rdb(in, n>>1))<<24 | uint32(rdb(in, n-1)) | uint32(n)<<8
			return avalanche64(uint64(combined) ^ uint64(rd32(sec, 0)^rd32(sec, 4)))
		}
		return avalanche64(rd64(sec, 56) ^ rd64(sec, 64))
	}
	if n <= 128 {
		// The rungs are spelled out here rather than called: at two
		// and a half nanoseconds the call to the ladder was the 7% by
		// which 17..32 bytes still trailed zeebo/xxh3.
		if n <= 32 {
			acc := uint64(n) * prime64_1
			acc += mix16BNS(in, sec) + mix16BNS(add(in, n-16), add(sec, 16))
			return avalanche(acc)
		}
		// Walk pairs of chunks inward from both ends. Keep one accumulator
		// chain: its loads arrive independently, and splitting the sum adds
		// a final dependency. That cost 5-8% on a Zen 4 at 64..128 bytes.
		//
		// Each rung's two mixes are summed before they reach the chain,
		// which the seeded core does not do. The instructions are the
		// same either way; what differs is how a Zen 4 steers them across
		// its integer schedulers, which moves single length classes by 5-10%
		// in either direction. Summed, this core measured 2.0% faster over
		// 17..128 bytes (geomean, every third length, four relinked
		// layouts) and no class slower; the seeded core measured 1.2% the
		// other way. Reorder either only with that sweep in hand.
		acc := uint64(n) * prime64_1
		if n > 64 {
			if n > 96 {
				acc += mix16BNS(add(in, 48), add(sec, 96)) + mix16BNS(add(in, n-64), add(sec, 112))
			}
			acc += mix16BNS(add(in, 32), add(sec, 64)) + mix16BNS(add(in, n-48), add(sec, 80))
		}
		acc += mix16BNS(add(in, 16), add(sec, 32)) + mix16BNS(add(in, n-32), add(sec, 48))
		acc += mix16BNS(in, sec) + mix16BNS(add(in, n-16), add(sec, 16))
		return avalanche(acc)
	}
	// The 129..240 ladder runs a fixed 8-chunk prologue, avalanches, then a
	// length-dependent tail; the mid-stream avalanche is what keeps it from
	// degenerating into a plain sum over many chunks. The prologue is split
	// across four partial sums, which are added at the end, so regrouping
	// cannot change the result: what it buys is eight independent multiplies
	// in flight instead of one chain of dependent adds. Each partial sum is
	// one line, so that no line holds a call and nothing else; see the note
	// on spelling in generic.go. That was three NOPs a hash, and 0.7% here
	// and 2.1% in the seeded core over 129..240 bytes on a Zen 4.
	//
	// It is inlined, unlike the 128-bit ladder: calling it cost 6% of a
	// 129..240-byte hash on a Zen 4, and 4% in the seeded core, and saved
	// nothing at any shorter length.
	if n <= midsizeMax {
		acc0 := uint64(n)*prime64_1 + mix16BNS(in, sec) + mix16BNS(add(in, 64), add(sec, 64))
		acc1 := mix16BNS(add(in, 16), add(sec, 16)) + mix16BNS(add(in, 80), add(sec, 80))
		acc2 := mix16BNS(add(in, 32), add(sec, 32)) + mix16BNS(add(in, 96), add(sec, 96))
		acc3 := mix16BNS(add(in, 48), add(sec, 48)) + mix16BNS(add(in, 112), add(sec, 112))
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
	var acc [accNB]uint64
	hashLong(&acc, in, int(n), sec, secretLen-stripeLen)
	// The convergence is written out rather than reached through
	// mergeAccs, which costs 287 nodes against the inliner's budget of
	// 80 and is therefore always a real call. mix2Accs is the largest
	// piece that does inline, so the four folds have to be named here.
	//
	// One call is all this saves, and on a Redwood Cove that measured
	// neutral -- within a percent either way at 256 and 512 bytes,
	// where the next hash's stripes overlap it. It is written this way
	// to match sum128NS, where the same change removes two calls and
	// is worth 9% at 256 bytes; see there. The seeded cores reach their
	// long path through sum64SeededLong, whose kernel hands back the
	// accumulators already keyed for the merge.
	s := add(sec, secretMergeAccsStart)
	m := uint64(n)*prime64_1 + mix2Accs(&acc, 0, s)
	m += mix2Accs(&acc, 2, add(s, 16)) + mix2Accs(&acc, 4, add(s, 32))
	return avalanche(m + mix2Accs(&acc, 6, add(s, 48)))
}

// sum64Seeded is the seeded core shared by Sum64Seed and short Digest reads,
// with routing and short cases in one call's worth of code. It used to call
// a separate secret-parameterized core, whose call and re-tested length
// tree was a third of an 8-byte seeded hash on a Zen 4. Up to 240 bytes the
// seed enters the arithmetic directly; above that XXH3 defines the seeded hash
// as the unseeded hash under a secret derived from the seed, which has to be
// built first. The seed-zero route keeps Sum64Seed(b, 0) == Sum64(b), at
// unseeded speed.
//
//go:nosplit
func sum64Seeded(in unsafe.Pointer, n uintptr, seed uint64) uint64 {
	if seed == 0 {
		return sum64SeedZero(in, n)
	}
	sec := unsafe.Pointer(&kSecret)
	if n <= 16 {
		// The short cases use the default secret and are checked against the
		// seeded reference vectors.
		if n > 8 {
			inputLo := rd64(in, 0) ^ ((rd64(sec, 24) ^ rd64(sec, 32)) + seed)
			inputHi := rd64(in, n-8) ^ ((rd64(sec, 40) ^ rd64(sec, 48)) - seed)
			acc := uint64(n) + bits.ReverseBytes64(inputLo) + inputHi + mul128Fold64(inputLo, inputHi)
			return avalanche(acc)
		}
		if n >= 4 {
			seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
			bitflip := (rd64(sec, 8) ^ rd64(sec, 16)) - seed
			return rrmxmx(uint64(rd32(in, n-4))+uint64(rd32(in, 0))<<32^bitflip, uint64(n))
		}
		if n > 0 {
			combined := uint32(rdb(in, 0))<<16 | uint32(rdb(in, n>>1))<<24 | uint32(rdb(in, n-1)) | uint32(n)<<8
			bitflip := uint64(rd32(sec, 0)^rd32(sec, 4)) + seed
			return avalanche64(uint64(combined) ^ bitflip)
		}
		return avalanche64(seed ^ rd64(sec, 56) ^ rd64(sec, 64))
	}
	if n <= 128 {
		// The 17..128 rungs inline, as in sum64NS; each mix pays the
		// seed's two adds and nothing else. The mixes join the chain one
		// at a time here, where sum64NS sums them in pairs; see there.
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
	if n <= midsizeMax {
		acc0 := uint64(n)*prime64_1 + mix16B(in, sec, seed) + mix16B(add(in, 64), add(sec, 64), seed)
		acc1 := mix16B(add(in, 16), add(sec, 16), seed) + mix16B(add(in, 80), add(sec, 80), seed)
		acc2 := mix16B(add(in, 32), add(sec, 32), seed) + mix16B(add(in, 96), add(sec, 96), seed)
		acc3 := mix16B(add(in, 48), add(sec, 48), seed) + mix16B(add(in, 112), add(sec, 112), seed)
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
	return sum64SeededLong(in, n, seed)
}

// ---------------------------------------------------------------------------
// 128-bit
// ---------------------------------------------------------------------------

// sum128NS is the 128-bit counterpart of sum64NS. Its short cases produce
// both halves from the start; the high half cannot be derived from the low.
// Keeping the seed arithmetic out has the same benefit as in sum64NS.
//
// Unlike sum64NS it tests the long cases first. The same code with the short
// cases first measured 11% slower at 33..64 bytes and 26% at 65..128 on a
// Zen 4, for no instruction it added: see sum64NS on how that core steers.
//
//go:nosplit
func sum128NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, secretLen int) Uint128 {
	if n > 16 {
		if n > midsizeMax {
			var acc [accNB]uint64
			hashLong(&acc, in, int(n), sec, secretLen-stripeLen)
			// Both convergences written out, for the reason given in sum64NS.
			// Here it removes two calls rather than one, and unlike there it
			// pays: 8.9% at 256 bytes on a Redwood Cove, 4.9% at a kibibyte
			// and 2.9% at 4 KiB. Two of these back to back are a long enough
			// serial tail that the next hash cannot hide them.
			s := add(sec, secretMergeAccsStart)
			lo := uint64(n)*prime64_1 + mix2Accs(&acc, 0, s)
			lo += mix2Accs(&acc, 2, add(s, 16)) + mix2Accs(&acc, 4, add(s, 32))
			t := add(sec, uintptr(secretLen-8*accNB-secretMergeAccsStart))
			hi := ^(uint64(n) * prime64_2) + mix2Accs(&acc, 0, t)
			hi += mix2Accs(&acc, 2, add(t, 16)) + mix2Accs(&acc, 4, add(t, 32))
			return Uint128{Lo: avalanche(lo + mix2Accs(&acc, 6, add(s, 48))), Hi: avalanche(hi + mix2Accs(&acc, 6, add(t, 48)))}
		}
		if n <= 128 {
			// The rungs inline, as in sum64NS. Each round loads its two
			// chunks' words once, for the keyed multiply and for the other
			// half's crossover, and sums them on the line that loads them;
			// see the note on spelling in generic.go.
			if n <= 32 {
				i0, i1, ix := rd64(in, 0), rd64(in, 8), rd64(in, 0)+rd64(in, 8)
				j0, j1, jx := rd64(in, n-16), rd64(in, n-8), rd64(in, n-16)+rd64(in, n-8)
				lo := (uint64(n)*prime64_1 + mul128Fold64(i0^rd64(sec, 0), i1^rd64(sec, 8))) ^ jx
				hi := mul128Fold64(j0^rd64(sec, 16), j1^rd64(sec, 24)) ^ ix
				return finalize128(lo, hi, n, 0)
			}
			lo := uint64(n) * prime64_1
			hi := uint64(0)
			if n > 64 {
				if n > 96 {
					j0, j1, jx := rd64(in, n-64), rd64(in, n-56), rd64(in, n-64)+rd64(in, n-56)
					i0, i1, ix := rd64(in, 48), rd64(in, 56), rd64(in, 48)+rd64(in, 56)
					hi = (hi + mul128Fold64(j0^rd64(sec, 112), j1^rd64(sec, 120))) ^ ix
					lo = (lo + mul128Fold64(i0^rd64(sec, 96), i1^rd64(sec, 104))) ^ jx
				}
				j0, j1, jx := rd64(in, n-48), rd64(in, n-40), rd64(in, n-48)+rd64(in, n-40)
				i0, i1, ix := rd64(in, 32), rd64(in, 40), rd64(in, 32)+rd64(in, 40)
				hi = (hi + mul128Fold64(j0^rd64(sec, 80), j1^rd64(sec, 88))) ^ ix
				lo = (lo + mul128Fold64(i0^rd64(sec, 64), i1^rd64(sec, 72))) ^ jx
			}
			{
				j0, j1, jx := rd64(in, n-32), rd64(in, n-24), rd64(in, n-32)+rd64(in, n-24)
				i0, i1, ix := rd64(in, 16), rd64(in, 24), rd64(in, 16)+rd64(in, 24)
				hi = (hi + mul128Fold64(j0^rd64(sec, 48), j1^rd64(sec, 56))) ^ ix
				lo = (lo + mul128Fold64(i0^rd64(sec, 32), i1^rd64(sec, 40))) ^ jx
			}
			{
				j0, j1, jx := rd64(in, n-16), rd64(in, n-8), rd64(in, n-16)+rd64(in, n-8)
				i0, i1, ix := rd64(in, 0), rd64(in, 8), rd64(in, 0)+rd64(in, 8)
				hi = (hi + mul128Fold64(j0^rd64(sec, 16), j1^rd64(sec, 24))) ^ ix
				lo = (lo + mul128Fold64(i0^rd64(sec, 0), i1^rd64(sec, 8))) ^ jx
			}
			return finalize128(lo, hi, n, 0)
		}
		return len129to240_128NS(in, n, sec)
	}
	// The short cases are written out here, as in sum64NS: each is a single
	// call's worth of work, so the call to reach an out-of-line version was
	// the largest removable part of its cost.
	if n > 8 {
		hi, lo := bits.Mul64(rd64(in, 0)^rd64(in, n-8)^(rd64(sec, 32)^rd64(sec, 40)), prime64_1)

		lo += uint64(n-1) << 54
		inputHi := rd64(in, n-8) ^ (rd64(sec, 48) ^ rd64(sec, 56))
		hi += inputHi + uint64(uint32(inputHi))*(prime32_2-1)
		lo ^= bits.ReverseBytes64(hi)

		rhi, rlo := bits.Mul64(lo, prime64_2)
		rhi += hi * prime64_2
		return Uint128{Lo: avalanche(rlo), Hi: avalanche(rhi)}
	}
	if n >= 4 {
		keyed := (uint64(rd32(in, 0)) + uint64(rd32(in, n-4))<<32) ^ (rd64(sec, 16) ^ rd64(sec, 24))

		hi, lo := bits.Mul64(keyed, prime64_1+uint64(n)<<2)
		hi += lo << 1
		lo ^= hi >> 3
		lo ^= lo >> 35
		lo *= 0x9FB21C651E98DF25
		lo ^= lo >> 28
		return Uint128{Lo: lo, Hi: avalanche(hi)}
	}
	if n > 0 {
		combinedl := uint32(rdb(in, 0))<<16 | uint32(rdb(in, n>>1))<<24 | uint32(rdb(in, n-1)) | uint32(n)<<8
		combinedh := bits.RotateLeft32(bits.ReverseBytes32(combinedl), 13)
		return Uint128{
			Lo: avalanche64(uint64(combinedl) ^ uint64(rd32(sec, 0)^rd32(sec, 4))),
			Hi: avalanche64(uint64(combinedh) ^ uint64(rd32(sec, 8)^rd32(sec, 12))),
		}
	}
	return Uint128{
		Lo: avalanche64(rd64(sec, 64) ^ rd64(sec, 72)),
		Hi: avalanche64(rd64(sec, 80) ^ rd64(sec, 88)),
	}
}

// sum64SeedZero relays Sum64Seed's zero-seed route to sum64NS from a
// splittable, non-inlined frame. Both the seeded twin and sum64NS are
// nosplit, and chained directly their frames run past the nosplit budget on
// 386; the shim's own stack check breaks the chain, at the cost of one call
// on a route only Sum64Seed(b, 0) takes.
//
//go:noinline
func sum64SeedZero(in unsafe.Pointer, n uintptr) uint64 {
	return sum64NS(in, n, unsafe.Pointer(&kSecret), secretDefaultSize)
}

// sum128SeedZero is sum64SeedZero's 128-bit counterpart.
//
//go:noinline
func sum128SeedZero(in unsafe.Pointer, n uintptr) Uint128 {
	return sum128NS(in, n, unsafe.Pointer(&kSecret), secretDefaultSize)
}

// sum64SeededLong hashes a seeded long input: XXH3 defines it as the unseeded
// hash under a secret derived from the seed. It is split out of sum64Seeded
// because the derived secret's frame does not fit a nosplit function once the
// race detector inflates it, and at this length the extra call is noise.
//
// Where there is a seeded kernel, and up to the length where it pays (see
// useSeedKernel), the secret is never derived into memory at all: the kernel
// applies the seed in registers and hands back the accumulators already
// xored with the merge's key, so all that is left here is the folds. Writing
// the secret out and reading it back with vector loads was the larger part
// of a short seeded long hash's cost; see SeededArch in the generator.
func sum64SeededLong(in unsafe.Pointer, n uintptr, seed uint64) uint64 {
	if useSeedKernel(n) {
		var k [2 * accNB]uint64
		hashLongSeed(&k, in, int(n), seed)
		m := uint64(n)*prime64_1 + mul128Fold64(k[0], k[1])
		m += mul128Fold64(k[2], k[3]) + mul128Fold64(k[4], k[5])
		return avalanche(m + mul128Fold64(k[6], k[7]))
	}
	var secret [secretDefaultSize]byte
	deriveSecret(&secret, seed)
	return sum64NS(in, n, unsafe.Pointer(&secret), secretDefaultSize)
}

// sum128SeededLong is sum64SeededLong's 128-bit counterpart, whose high half
// folds the accumulators keyed for the second merge.
func sum128SeededLong(in unsafe.Pointer, n uintptr, seed uint64) Uint128 {
	if useSeedKernel(n) {
		var k [2 * accNB]uint64
		hashLongSeed(&k, in, int(n), seed)
		lo := uint64(n)*prime64_1 + mul128Fold64(k[0], k[1])
		lo += mul128Fold64(k[2], k[3]) + mul128Fold64(k[4], k[5])
		hi := ^(uint64(n) * prime64_2) + mul128Fold64(k[8], k[9])
		hi += mul128Fold64(k[10], k[11]) + mul128Fold64(k[12], k[13])
		return Uint128{Lo: avalanche(lo + mul128Fold64(k[6], k[7])), Hi: avalanche(hi + mul128Fold64(k[14], k[15]))}
	}
	var secret [secretDefaultSize]byte
	deriveSecret(&secret, seed)
	return sum128NS(in, n, unsafe.Pointer(&secret), secretDefaultSize)
}

// sum128Seeded is sum64Seeded's 128-bit counterpart; see the comment there.
//
//go:nosplit
func sum128Seeded(in unsafe.Pointer, n uintptr, seed uint64) Uint128 {
	if seed == 0 {
		return sum128SeedZero(in, n)
	}
	sec := unsafe.Pointer(&kSecret)
	if n <= 16 {
		// The short cases use the default secret, as in sum64Seeded.
		if n > 8 {
			bitflipl := (rd64(sec, 32) ^ rd64(sec, 40)) - seed
			hi, lo := bits.Mul64(rd64(in, 0)^rd64(in, n-8)^bitflipl, prime64_1)

			lo += uint64(n-1) << 54
			inputHi := rd64(in, n-8) ^ ((rd64(sec, 48) ^ rd64(sec, 56)) + seed)
			hi += inputHi + uint64(uint32(inputHi))*(prime32_2-1)
			lo ^= bits.ReverseBytes64(hi)

			rhi, rlo := bits.Mul64(lo, prime64_2)
			rhi += hi * prime64_2
			return Uint128{Lo: avalanche(rlo), Hi: avalanche(rhi)}
		}
		if n >= 4 {
			seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
			keyed := (uint64(rd32(in, 0)) + uint64(rd32(in, n-4))<<32) ^ ((rd64(sec, 16) ^ rd64(sec, 24)) + seed)

			hi, lo := bits.Mul64(keyed, prime64_1+uint64(n)<<2)
			hi += lo << 1
			lo ^= hi >> 3
			lo ^= lo >> 35
			lo *= 0x9FB21C651E98DF25
			lo ^= lo >> 28
			return Uint128{Lo: lo, Hi: avalanche(hi)}
		}
		if n > 0 {
			combinedl := uint32(rdb(in, 0))<<16 | uint32(rdb(in, n>>1))<<24 | uint32(rdb(in, n-1)) | uint32(n)<<8
			combinedh := bits.RotateLeft32(bits.ReverseBytes32(combinedl), 13)
			return Uint128{
				Lo: avalanche64(uint64(combinedl) ^ (uint64(rd32(sec, 0)^rd32(sec, 4)) + seed)),
				Hi: avalanche64(uint64(combinedh) ^ (uint64(rd32(sec, 8)^rd32(sec, 12)) - seed)),
			}
		}
		return Uint128{
			Lo: avalanche64(seed ^ rd64(sec, 64) ^ rd64(sec, 72)),
			Hi: avalanche64(seed ^ rd64(sec, 80) ^ rd64(sec, 88)),
		}
	}
	if n > midsizeMax {
		return sum128SeededLong(in, n, seed)
	}
	if n <= 128 {
		// The 17..32 rung and the 33..128 rungs inline, as in sum128NS,
		// with the seed keying each secret word.
		//
		// Unlike sum128NS, the loads sit on lines of their own, and each
		// such line executes a NOP; see the note on spelling in
		// generic.go. Spelled as in sum128NS, nine instructions fewer at
		// 86 bytes, this core measured 10% slower at 33..64 bytes and 15%
		// at 65..128 on a Zen 4, stalled on integer scheduler tokens: 3.9
		// and 2.5 cycles a hash in the first two queues against 0.1 and
		// 0.2. The NOPs change how the multiplies are spread across the
		// schedulers. Measure before tidying them away.
		if n <= 32 {
			i0, i1 := rd64(in, 0), rd64(in, 8)
			j0, j1 := rd64(add(in, n-16), 0), rd64(add(in, n-16), 8)
			lo := (uint64(n)*prime64_1 + mul128Fold64(i0^(rd64(sec, 0)+seed), i1^(rd64(sec, 8)-seed))) ^ (j0 + j1)
			hi := mul128Fold64(j0^(rd64(sec, 16)+seed), j1^(rd64(sec, 24)-seed)) ^ (i0 + i1)
			return finalize128(lo, hi, n, seed)
		}
		lo := uint64(n) * prime64_1
		hi := uint64(0)
		if n > 64 {
			if n > 96 {
				j0, j1 := rd64(add(in, n-64), 0), rd64(add(in, n-64), 8)
				i0, i1 := rd64(add(in, 48), 0), rd64(add(in, 48), 8)
				hi = (hi + mul128Fold64(j0^(rd64(add(sec, 96+16), 0)+seed), j1^(rd64(add(sec, 96+16), 8)-seed))) ^ (i0 + i1)
				lo = (lo + mul128Fold64(i0^(rd64(add(sec, 96), 0)+seed), i1^(rd64(add(sec, 96), 8)-seed))) ^ (j0 + j1)
			}
			j0, j1 := rd64(add(in, n-48), 0), rd64(add(in, n-48), 8)
			i0, i1 := rd64(add(in, 32), 0), rd64(add(in, 32), 8)
			hi = (hi + mul128Fold64(j0^(rd64(add(sec, 64+16), 0)+seed), j1^(rd64(add(sec, 64+16), 8)-seed))) ^ (i0 + i1)
			lo = (lo + mul128Fold64(i0^(rd64(add(sec, 64), 0)+seed), i1^(rd64(add(sec, 64), 8)-seed))) ^ (j0 + j1)
		}
		{
			j0, j1 := rd64(add(in, n-32), 0), rd64(add(in, n-32), 8)
			i0, i1 := rd64(add(in, 16), 0), rd64(add(in, 16), 8)
			hi = (hi + mul128Fold64(j0^(rd64(add(sec, 32+16), 0)+seed), j1^(rd64(add(sec, 32+16), 8)-seed))) ^ (i0 + i1)
			lo = (lo + mul128Fold64(i0^(rd64(add(sec, 32), 0)+seed), i1^(rd64(add(sec, 32), 8)-seed))) ^ (j0 + j1)
		}
		{
			j0, j1 := rd64(add(in, n-16), 0), rd64(add(in, n-16), 8)
			i0, i1 := rd64(in, 0), rd64(in, 8)
			hi = (hi + mul128Fold64(j0^(rd64(add(sec, 16), 0)+seed), j1^(rd64(add(sec, 16), 8)-seed))) ^ (i0 + i1)
			lo = (lo + mul128Fold64(i0^(rd64(sec, 0)+seed), i1^(rd64(sec, 8)-seed))) ^ (j0 + j1)
		}
		return finalize128(lo, hi, n, seed)
	}
	return len129to240_128(in, n, sec, seed)
}
