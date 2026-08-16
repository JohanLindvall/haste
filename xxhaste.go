package xxhaste

import (
	"math/bits"
	"unsafe"
)

// The public entry points are deliberately thin: each is small enough for the
// compiler to inline into its caller, so hashing a short key costs one call,
// to sum64 or sum128. That matters because the shortest inputs take about ten
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
// A seed does not change the cost for inputs up to 240 bytes. Beyond that it
// forces a 192-byte secret to be derived per call, so a caller hashing many
// long inputs under one seed is better served by a Digest from NewSeed.
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
		panic("xxhaste: secret shorter than MinSecretSize")
	}
}

// ---------------------------------------------------------------------------
// 64-bit
// ---------------------------------------------------------------------------

// sum64 hashes n bytes at in under the secret at sec.
//
// The four cases up to 16 bytes are written out here instead of being called,
// which is what keeps a short hash to a single call. They are transcriptions
// of XXH3_len_1to3_64b, len_4to8, len_9to16 and the empty-input case; each
// keys the input with a different pair of secret words so that no two length
// classes can collide through the same bits.
//
// It is nosplit because the stack-growth check in the prologue is a
// measurable share of a ten-cycle hash. The frame is 112 bytes, well inside
// the nosplit budget, and the linker verifies that at build time.
//
//go:nosplit
func sum64(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, secretLen int, seed uint64) uint64 {
	if n > 16 {
		if n > midsizeMax {
			// The accumulator path is spelled out here rather than behind two
			// more calls: at this length the calls are a measurable share of
			// the cost, and 256 bytes is only four stripes of work.
			acc := initAcc
			hashLong(&acc, in, int(n), sec, secretLen-stripeLen)
			return mergeAccs(&acc, add(sec, secretMergeAccsStart), uint64(n)*prime64_1)
		}
		// The ladders are reached from here for the same reason. Neither is
		// small enough to inline, so anything between them and sum64 is a real
		// call -- worth 10% at 128 bytes and 19% at 32. The unseeded twins
		// exist because a seed costs two adds in every mix, and most hashing
		// has none; the branch predicts perfectly either way.
		if n <= 128 {
			if seed == 0 {
				return len17to128_64NS(in, n, sec)
			}
			return len17to128_64(in, n, sec, seed)
		}
		if seed == 0 {
			return len129to240_64NS(in, n, sec)
		}
		return len129to240_64(in, n, sec, seed)
	}
	if n > 8 {
		// 9..16 bytes: the two overlapping halves are folded through the
		// 128-bit multiply, with the length mixed in so that repeats of a
		// shorter input cannot reproduce a longer one.
		bitflip1 := (rd64(sec, 24) ^ rd64(sec, 32)) + seed
		bitflip2 := (rd64(sec, 40) ^ rd64(sec, 48)) - seed
		inputLo := rd64(in, 0) ^ bitflip1
		inputHi := rd64(in, n-8) ^ bitflip2
		acc := uint64(n) + bits.ReverseBytes64(inputLo) + inputHi + mul128Fold64(inputLo, inputHi)
		return avalanche(acc)
	}
	if n >= 4 {
		// 4..8 bytes: the halves are swapped into one 64-bit word before
		// keying, and rrmxmx folds the length in.
		seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
		in1 := rd32(in, 0)
		in2 := rd32(in, n-4)
		bitflip := (rd64(sec, 8) ^ rd64(sec, 16)) - seed
		return rrmxmx(uint64(in2)+uint64(in1)<<32^bitflip, uint64(n))
	}
	if n > 0 {
		// 1..3 bytes: first, middle and last byte plus the length, which is
		// what keeps 1-byte and 2-byte inputs of the same byte apart.
		c1 := uint32(rdb(in, 0))
		c2 := uint32(rdb(in, n>>1))
		c3 := uint32(rdb(in, n-1))
		combined := c1<<16 | c2<<24 | c3 | uint32(n)<<8
		bitflip := uint64(rd32(sec, 0)^rd32(sec, 4)) + seed
		return avalanche64(uint64(combined) ^ bitflip)
	}
	return avalanche64(seed ^ rd64(sec, 56) ^ rd64(sec, 64))
}

// sum64NS is sum64 with the seed arithmetic gone, for the unseeded entry
// points. A seed enters each short case as one to four instructions -- the
// 4..8 case spends a byte-reverse, a shift, a xor and a subtract deriving its
// mix -- and on a core that is dispatch-saturated here, dead instructions are
// the whole cost. The twins route straight to the seed-free ladders, so the
// unseeded path never tests the seed at all. Held to the same vectors as
// sum64: seed-zero cases go through here and must be bit-identical.
//
//go:nosplit
func sum64NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, secretLen int) uint64 {
	if n > 16 {
		if n > midsizeMax {
			acc := initAcc
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
			// is worth 9% at 256 bytes; see there. The seeded twins keep the
			// call: past 240 bytes a seed derives a 192-byte secret first,
			// which costs far more than either.
			s := add(sec, secretMergeAccsStart)
			m0 := mix2Accs(&acc, 0, s)
			m1 := mix2Accs(&acc, 2, add(s, 16))
			m2 := mix2Accs(&acc, 4, add(s, 32))
			m3 := mix2Accs(&acc, 6, add(s, 48))
			return avalanche((uint64(n)*prime64_1 + m0) + (m1 + m2) + m3)
		}
		if n <= 128 {
			// The 17..32 rung is spelled out here rather than called: it is
			// two mixes, and at two and a half nanoseconds the call to the
			// ladder was the 7% by which this size still trailed. The longer
			// rungs keep the call -- the same overhead shrinks into
			// insignificance against four or more mixes.
			if n <= 32 {
				acc := uint64(n) * prime64_1
				acc += mix16BNS(in, sec)
				acc += mix16BNS(add(in, n-16), add(sec, 16))
				return avalanche(acc)
			}
			// The 33..128 rungs, hand-inlined; see len17to128_64NS.
			acc := uint64(n) * prime64_1
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
			acc += mix16BNS(in, sec)
			acc += mix16BNS(add(in, n-16), add(sec, 16))
			return avalanche(acc)
		}
		return len129to240_64NS(in, n, sec)
	}
	if n > 8 {
		inputLo := rd64(in, 0) ^ (rd64(sec, 24) ^ rd64(sec, 32))
		inputHi := rd64(in, n-8) ^ (rd64(sec, 40) ^ rd64(sec, 48))
		acc := uint64(n) + bits.ReverseBytes64(inputLo) + inputHi + mul128Fold64(inputLo, inputHi)
		return avalanche(acc)
	}
	if n >= 4 {
		in1 := rd32(in, 0)
		in2 := rd32(in, n-4)
		bitflip := rd64(sec, 8) ^ rd64(sec, 16)
		return rrmxmx(uint64(in2)+uint64(in1)<<32^bitflip, uint64(n))
	}
	if n > 0 {
		c1 := uint32(rdb(in, 0))
		c2 := uint32(rdb(in, n>>1))
		c3 := uint32(rdb(in, n-1))
		combined := c1<<16 | c2<<24 | c3 | uint32(n)<<8
		return avalanche64(uint64(combined) ^ uint64(rd32(sec, 0)^rd32(sec, 4)))
	}
	return avalanche64(rd64(sec, 56) ^ rd64(sec, 64))
}

// sum64Seeded is the seeded core behind Sum64Seed: sum64 under the default
// secret, with the routing and the short cases in one call's worth of code. It
// used to route through sum64, and the second call plus its re-tested length
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
	if n > 16 {
		if n > midsizeMax {
			return sum64SeededLong(in, n, seed)
		}
		if n <= 128 {
			// The 17..128 rungs inline, as in sum64NS; each mix pays the
			// seed's two adds and nothing else.
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
		return len129to240_64(in, n, sec, seed)
	}
	// The short cases, transcribed from sum64 with the default secret's
	// pointer; kept in lockstep with it by the seeded reference vectors.
	if n > 8 {
		bitflip1 := (rd64(sec, 24) ^ rd64(sec, 32)) + seed
		bitflip2 := (rd64(sec, 40) ^ rd64(sec, 48)) - seed
		inputLo := rd64(in, 0) ^ bitflip1
		inputHi := rd64(in, n-8) ^ bitflip2
		acc := uint64(n) + bits.ReverseBytes64(inputLo) + inputHi + mul128Fold64(inputLo, inputHi)
		return avalanche(acc)
	}
	if n >= 4 {
		seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
		in1 := rd32(in, 0)
		in2 := rd32(in, n-4)
		bitflip := (rd64(sec, 8) ^ rd64(sec, 16)) - seed
		return rrmxmx(uint64(in2)+uint64(in1)<<32^bitflip, uint64(n))
	}
	if n > 0 {
		c1 := uint32(rdb(in, 0))
		c2 := uint32(rdb(in, n>>1))
		c3 := uint32(rdb(in, n-1))
		combined := c1<<16 | c2<<24 | c3 | uint32(n)<<8
		bitflip := uint64(rd32(sec, 0)^rd32(sec, 4)) + seed
		return avalanche64(uint64(combined) ^ bitflip)
	}
	return avalanche64(seed ^ rd64(sec, 56) ^ rd64(sec, 64))
}

// ---------------------------------------------------------------------------
// 128-bit
// ---------------------------------------------------------------------------

// sum128 is sum64's counterpart. The short cases are not the 64-bit ones with
// a second half bolted on: each produces both halves from the start, so that
// neither can be derived from the other. It is nosplit for the reason given on
// sum64.
//
//go:nosplit
func sum128(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, secretLen int, seed uint64) Uint128 {
	if n > 16 {
		if n > midsizeMax {
			// As in sum64, and worth more here: this path converges the
			// accumulators twice.
			acc := initAcc
			hashLong(&acc, in, int(n), sec, secretLen-stripeLen)
			return Uint128{
				Lo: mergeAccs(&acc, add(sec, secretMergeAccsStart), uint64(n)*prime64_1),
				Hi: mergeAccs(&acc, add(sec, uintptr(secretLen-8*accNB-secretMergeAccsStart)),
					^(uint64(n) * prime64_2)),
			}
		}
		// And the ladders directly, as in sum64.
		if n <= 128 {
			if seed == 0 {
				return len17to128_128NS(in, n, sec)
			}
			return len17to128_128(in, n, sec, seed)
		}
		if seed == 0 {
			return len129to240_128NS(in, n, sec)
		}
		return len129to240_128(in, n, sec, seed)
	}
	// Written out as in sum128NS; the seeded forms differ only in keying.
	if n > 8 {
		bitflipl := (rd64(sec, 32) ^ rd64(sec, 40)) - seed
		bitfliph := (rd64(sec, 48) ^ rd64(sec, 56)) + seed
		inputLo := rd64(in, 0)
		inputHi := rd64(in, n-8)
		hi, lo := bits.Mul64(inputLo^inputHi^bitflipl, prime64_1)

		lo += uint64(n-1) << 54
		inputHi ^= bitfliph
		hi += inputHi + uint64(uint32(inputHi))*(prime32_2-1)
		lo ^= bits.ReverseBytes64(hi)

		rhi, rlo := bits.Mul64(lo, prime64_2)
		rhi += hi * prime64_2
		return Uint128{Lo: avalanche(rlo), Hi: avalanche(rhi)}
	}
	if n >= 4 {
		seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
		inputLo := rd32(in, 0)
		inputHi := rd32(in, n-4)
		keyed := (uint64(inputLo) + uint64(inputHi)<<32) ^ ((rd64(sec, 16) ^ rd64(sec, 24)) + seed)

		hi, lo := bits.Mul64(keyed, prime64_1+uint64(n)<<2)
		hi += lo << 1
		lo ^= hi >> 3
		lo = xorshift64(lo, 35)
		lo *= 0x9FB21C651E98DF25
		lo = xorshift64(lo, 28)
		return Uint128{Lo: lo, Hi: avalanche(hi)}
	}
	if n > 0 {
		c1 := uint32(rdb(in, 0))
		c2 := uint32(rdb(in, n>>1))
		c3 := uint32(rdb(in, n-1))
		combinedl := c1<<16 | c2<<24 | c3 | uint32(n)<<8
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

// sum128NS is sum128 with the seed arithmetic gone; see sum64NS. The short
// cases are spelled out here rather than calling the seeded functions with a
// zero, because the zero still costs its instructions.
//
//go:nosplit
func sum128NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, secretLen int) Uint128 {
	if n > 16 {
		if n > midsizeMax {
			acc := initAcc
			hashLong(&acc, in, int(n), sec, secretLen-stripeLen)
			// Both convergences written out, for the reason given in sum64NS.
			// Here it removes two calls rather than one, and unlike there it
			// pays: 8.9% at 256 bytes on a Redwood Cove, 4.9% at a kibibyte
			// and 2.9% at 4 KiB. Two of these back to back are a long enough
			// serial tail that the next hash cannot hide them.
			s := add(sec, secretMergeAccsStart)
			l0 := mix2Accs(&acc, 0, s)
			l1 := mix2Accs(&acc, 2, add(s, 16))
			l2 := mix2Accs(&acc, 4, add(s, 32))
			l3 := mix2Accs(&acc, 6, add(s, 48))
			t := add(sec, uintptr(secretLen-8*accNB-secretMergeAccsStart))
			h0 := mix2Accs(&acc, 0, t)
			h1 := mix2Accs(&acc, 2, add(t, 16))
			h2 := mix2Accs(&acc, 4, add(t, 32))
			h3 := mix2Accs(&acc, 6, add(t, 48))
			return Uint128{
				Lo: avalanche((uint64(n)*prime64_1 + l0) + (l1 + l2) + l3),
				Hi: avalanche((^(uint64(n) * prime64_2) + h0) + (h1 + h2) + h3),
			}
		}
		if n <= 128 {
			// The 17..32 rung inline, as in sum64NS, with each input word
			// loaded once; see len17to128_128NS.
			if n <= 32 {
				i0, i1 := rd64(in, 0), rd64(in, 8)
				j0, j1 := rd64(add(in, n-16), 0), rd64(add(in, n-16), 8)
				lo := (uint64(n)*prime64_1 + mul128Fold64(i0^rd64(sec, 0), i1^rd64(sec, 8))) ^ (j0 + j1)
				hi := mul128Fold64(j0^rd64(sec, 16), j1^rd64(sec, 24)) ^ (i0 + i1)
				return finalize128(lo, hi, n, 0)
			}
			// The 33..128 rungs, hand-inlined; see len17to128_128NS.
			lo := uint64(n) * prime64_1
			hi := uint64(0)
			if n > 64 {
				if n > 96 {
					j0, j1 := rd64(add(in, n-64), 0), rd64(add(in, n-64), 8)
					i0, i1 := rd64(add(in, 48), 0), rd64(add(in, 48), 8)
					hi = (hi + mul128Fold64(j0^rd64(add(sec, 96+16), 0), j1^rd64(add(sec, 96+16), 8))) ^ (i0 + i1)
					lo = (lo + mul128Fold64(i0^rd64(add(sec, 96), 0), i1^rd64(add(sec, 96), 8))) ^ (j0 + j1)
				}
				j0, j1 := rd64(add(in, n-48), 0), rd64(add(in, n-48), 8)
				i0, i1 := rd64(add(in, 32), 0), rd64(add(in, 32), 8)
				hi = (hi + mul128Fold64(j0^rd64(add(sec, 64+16), 0), j1^rd64(add(sec, 64+16), 8))) ^ (i0 + i1)
				lo = (lo + mul128Fold64(i0^rd64(add(sec, 64), 0), i1^rd64(add(sec, 64), 8))) ^ (j0 + j1)
			}
			{
				j0, j1 := rd64(add(in, n-32), 0), rd64(add(in, n-32), 8)
				i0, i1 := rd64(add(in, 16), 0), rd64(add(in, 16), 8)
				hi = (hi + mul128Fold64(j0^rd64(add(sec, 32+16), 0), j1^rd64(add(sec, 32+16), 8))) ^ (i0 + i1)
				lo = (lo + mul128Fold64(i0^rd64(add(sec, 32), 0), i1^rd64(add(sec, 32), 8))) ^ (j0 + j1)
			}
			{
				j0, j1 := rd64(add(in, n-16), 0), rd64(add(in, n-16), 8)
				i0, i1 := rd64(in, 0), rd64(in, 8)
				hi = (hi + mul128Fold64(j0^rd64(add(sec, 16), 0), j1^rd64(add(sec, 16), 8))) ^ (i0 + i1)
				lo = (lo + mul128Fold64(i0^rd64(sec, 0), i1^rd64(sec, 8))) ^ (j0 + j1)
			}
			return finalize128(lo, hi, n, 0)
		}
		return len129to240_128NS(in, n, sec)
	}
	// The short cases are written out here, as in sum64NS: each is a single
	// call's worth of work, so the call to reach an out-of-line version was
	// the largest removable part of its cost.
	if n > 8 {
		bitfliph := rd64(sec, 48) ^ rd64(sec, 56)
		inputLo := rd64(in, 0)
		inputHi := rd64(in, n-8)
		hi, lo := bits.Mul64(inputLo^inputHi^(rd64(sec, 32)^rd64(sec, 40)), prime64_1)

		lo += uint64(n-1) << 54
		inputHi ^= bitfliph
		hi += inputHi + uint64(uint32(inputHi))*(prime32_2-1)
		lo ^= bits.ReverseBytes64(hi)

		rhi, rlo := bits.Mul64(lo, prime64_2)
		rhi += hi * prime64_2
		return Uint128{Lo: avalanche(rlo), Hi: avalanche(rhi)}
	}
	if n >= 4 {
		inputLo := rd32(in, 0)
		inputHi := rd32(in, n-4)
		keyed := (uint64(inputLo) + uint64(inputHi)<<32) ^ (rd64(sec, 16) ^ rd64(sec, 24))

		hi, lo := bits.Mul64(keyed, prime64_1+uint64(n)<<2)
		hi += lo << 1
		lo ^= hi >> 3
		lo = xorshift64(lo, 35)
		lo *= 0x9FB21C651E98DF25
		lo = xorshift64(lo, 28)
		return Uint128{Lo: lo, Hi: avalanche(hi)}
	}
	if n > 0 {
		c1 := uint32(rdb(in, 0))
		c2 := uint32(rdb(in, n>>1))
		c3 := uint32(rdb(in, n-1))
		combinedl := c1<<16 | c2<<24 | c3 | uint32(n)<<8
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
func sum64SeededLong(in unsafe.Pointer, n uintptr, seed uint64) uint64 {
	var secret [secretDefaultSize]byte
	deriveSecret(&secret, seed)
	return sum64NS(in, n, unsafe.Pointer(&secret), secretDefaultSize)
}

// sum128SeededLong is sum64SeededLong's 128-bit counterpart.
func sum128SeededLong(in unsafe.Pointer, n uintptr, seed uint64) Uint128 {
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
	if n > 16 {
		if n > midsizeMax {
			return sum128SeededLong(in, n, seed)
		}
		if n <= 128 {
			// The 17..32 rung and the 33..128 rungs inline, as in sum128NS,
			// with the seed keying each secret word.
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
	// The short cases, transcribed from sum128; see sum64Seeded.
	if n > 8 {
		bitflipl := (rd64(sec, 32) ^ rd64(sec, 40)) - seed
		bitfliph := (rd64(sec, 48) ^ rd64(sec, 56)) + seed
		inputLo := rd64(in, 0)
		inputHi := rd64(in, n-8)
		hi, lo := bits.Mul64(inputLo^inputHi^bitflipl, prime64_1)

		lo += uint64(n-1) << 54
		inputHi ^= bitfliph
		hi += inputHi + uint64(uint32(inputHi))*(prime32_2-1)
		lo ^= bits.ReverseBytes64(hi)

		rhi, rlo := bits.Mul64(lo, prime64_2)
		rhi += hi * prime64_2
		return Uint128{Lo: avalanche(rlo), Hi: avalanche(rhi)}
	}
	if n >= 4 {
		seed ^= uint64(bits.ReverseBytes32(uint32(seed))) << 32
		inputLo := rd32(in, 0)
		inputHi := rd32(in, n-4)
		keyed := (uint64(inputLo) + uint64(inputHi)<<32) ^ ((rd64(sec, 16) ^ rd64(sec, 24)) + seed)

		hi, lo := bits.Mul64(keyed, prime64_1+uint64(n)<<2)
		hi += lo << 1
		lo ^= hi >> 3
		lo = xorshift64(lo, 35)
		lo *= 0x9FB21C651E98DF25
		lo = xorshift64(lo, 28)
		return Uint128{Lo: lo, Hi: avalanche(hi)}
	}
	if n > 0 {
		c1 := uint32(rdb(in, 0))
		c2 := uint32(rdb(in, n>>1))
		c3 := uint32(rdb(in, n-1))
		combinedl := c1<<16 | c2<<24 | c3 | uint32(n)<<8
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
