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
// in the seeded and unseeded cores in xxh3.go.

package xxh3

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

	// lastRound is where the 128-bit ladders key their final, reversed
	// round: the 64-bit ladder's last chunk and the sixteen bytes before it.
	lastRound = secretSizeMin - midsizeLastOffset - 16

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
	// one write: a Write that does not fill it costs one copy, and only the
	// one that overflows pays for absorb, the secret pointer, the block
	// position and the kernel's own prologue. Doubling it from the
	// reference's 256 bytes was worth 17% on 64-byte writes and 19% on
	// 256-byte ones on a Neoverse N2.
	//
	// A block is what it is now, which is also what zeebo/xxh3 stages. On a
	// Redwood Cove that was worth another 17.7% at 64-byte writes, 11.6% at
	// 256 and 3.5% at 4 KiB, and cost nothing at a kibibyte; a further
	// doubling was better again below 64 bytes but lost at 256 and above,
	// which is the trade CLAUDE.md records against going past 512 on the N2.
	// That measurement predates this value and has not been repeated -- see
	// the streaming notes there before changing it back.
	//
	// It is the one tuning parameter that shows outside the package:
	// marshaledSize counts it, so a state marshalled by a build with a
	// different value is rejected by UnmarshalBinary rather than restored.
	internalBufferSize = 1024
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

// A note on spelling, which applies to every small helper in this package:
// an inlined call leaves an inline mark, which the compiler turns into a
// real one-byte NOP unless some instruction of its own sits on the calling
// line. A helper that calls another helper on a line of its own -- the way
// avalanche once called a xorshift64 for each of its shifts -- therefore
// executes a NOP per call, and so does a line that does nothing but load
// through rd64. On a Zen 4 the short paths dispatch at five ops a cycle,
// so those NOPs were slots: two per avalanche, and up to ten per 128-bit
// hash. The helpers are written flat, and each call sits on a line that
// does arithmetic of its own.
//
// Two places keep their NOPs on purpose, sum128Seeded's 17..128 rungs and
// len129to240_128NS: on a Zen 4 the same code without them measured 5-15%
// slower over the lengths they serve, stalled on integer scheduler tokens
// in one or two of the four queues while the others idled. How ops are
// spread across the queues follows their order in the stream, and the NOPs
// happen to spread the multiplies. Each says so where it is. So a spelling
// change here is judged across a sweep of lengths, not by its NOP count.

// avalanche is XXH3's own finalizer, used wherever a value has already been
// well mixed and only needs its bits spread.
func avalanche(h uint64) uint64 {
	h ^= h >> 37
	h *= 0x165667919E3779F9
	return h ^ h>>32
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
	return h ^ h>>28
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

// mix16B consumes 16 bytes of input against 16 bytes of secret. The call is
// one line on purpose: split across three, its line held no instruction and
// every mix16B executed a NOP.
func mix16B(in, sec unsafe.Pointer, seed uint64) uint64 {
	return mul128Fold64(rd64(in, 0)^(rd64(sec, 0)+seed), rd64(in, 8)^(rd64(sec, 8)-seed))
}

// ---------------------------------------------------------------------------
// Mid-size inputs
//
// Everything up to 240 bytes lives in the cores in xxh3.go, where a call would
// show up in the measurement, except the 128-bit 129..240 ladders: they are
// long enough to amortize one, and inlined into the seeded core the ladder
// cost more at shorter lengths than it saved.
// ---------------------------------------------------------------------------

// finalize128 converges the two accumulator halves into a 128-bit result. The
// high half is negated so that it cannot equal the low half for any input.
func finalize128(lo, hi uint64, length uintptr, seed uint64) Uint128 {
	return Uint128{
		Lo: avalanche(lo + hi),
		Hi: -avalanche(lo*prime64_1 + hi*prime64_4 + (uint64(length)-seed)*prime64_2),
	}
}

// len129to240_128 runs four fixed 32-byte rounds, avalanches both halves, then
// a length-dependent tail. Each round keys one 16-byte chunk into each half and
// crosses in the other chunk's two words added, which is what makes each half
// of the 128-bit hash depend on both chunks.
//
// The rounds and the tail are both written out: Go does not unroll even a
// constant-count loop, and a round whose offsets are immediates issues
// measurably denser than one that computes them. The tail's length tests
// replicate the reference loop's bound in its order, so the hash cannot move.
// Each round sums its chunks' words on the line that loads them, which keeps
// those lines from executing a NOP apiece; see the spelling note above.
//
// It is a call rather than inlined into sum128Seeded: inlined, it measured
// 1% faster at 129..240 bytes on a Zen 4 and 1-3% slower at 17..64, and its
// frame put that nosplit function over the linker's budget on 386 and mips64.
func len129to240_128(in unsafe.Pointer, n uintptr, sec unsafe.Pointer, seed uint64) Uint128 {
	lo := uint64(n) * prime64_1
	hi := uint64(0)
	{
		i0, i1, ix := rd64(in, 0), rd64(in, 8), rd64(in, 0)+rd64(in, 8)
		j0, j1, jx := rd64(in, 16), rd64(in, 24), rd64(in, 16)+rd64(in, 24)
		lo = (lo + mul128Fold64(i0^(rd64(sec, 0)+seed), i1^(rd64(sec, 8)-seed))) ^ jx
		hi = (hi + mul128Fold64(j0^(rd64(sec, 16)+seed), j1^(rd64(sec, 24)-seed))) ^ ix
	}
	{
		i0, i1, ix := rd64(in, 32), rd64(in, 40), rd64(in, 32)+rd64(in, 40)
		j0, j1, jx := rd64(in, 48), rd64(in, 56), rd64(in, 48)+rd64(in, 56)
		lo = (lo + mul128Fold64(i0^(rd64(sec, 32)+seed), i1^(rd64(sec, 40)-seed))) ^ jx
		hi = (hi + mul128Fold64(j0^(rd64(sec, 48)+seed), j1^(rd64(sec, 56)-seed))) ^ ix
	}
	{
		i0, i1, ix := rd64(in, 64), rd64(in, 72), rd64(in, 64)+rd64(in, 72)
		j0, j1, jx := rd64(in, 80), rd64(in, 88), rd64(in, 80)+rd64(in, 88)
		lo = (lo + mul128Fold64(i0^(rd64(sec, 64)+seed), i1^(rd64(sec, 72)-seed))) ^ jx
		hi = (hi + mul128Fold64(j0^(rd64(sec, 80)+seed), j1^(rd64(sec, 88)-seed))) ^ ix
	}
	{
		i0, i1, ix := rd64(in, 96), rd64(in, 104), rd64(in, 96)+rd64(in, 104)
		j0, j1, jx := rd64(in, 112), rd64(in, 120), rd64(in, 112)+rd64(in, 120)
		lo = avalanche((lo + mul128Fold64(i0^(rd64(sec, 96)+seed), i1^(rd64(sec, 104)-seed))) ^ jx)
		hi = avalanche((hi + mul128Fold64(j0^(rd64(sec, 112)+seed), j1^(rd64(sec, 120)-seed))) ^ ix)
	}
	if n >= 160 {
		i0, i1, ix := rd64(in, 128), rd64(in, 136), rd64(in, 128)+rd64(in, 136)
		j0, j1, jx := rd64(in, 144), rd64(in, 152), rd64(in, 144)+rd64(in, 152)
		lo = (lo + mul128Fold64(i0^(rd64(sec, midsizeStartOffset)+seed), i1^(rd64(sec, midsizeStartOffset+8)-seed))) ^ jx
		hi = (hi + mul128Fold64(j0^(rd64(sec, midsizeStartOffset+16)+seed), j1^(rd64(sec, midsizeStartOffset+24)-seed))) ^ ix
		if n >= 192 {
			i0, i1, ix := rd64(in, 160), rd64(in, 168), rd64(in, 160)+rd64(in, 168)
			j0, j1, jx := rd64(in, 176), rd64(in, 184), rd64(in, 176)+rd64(in, 184)
			lo = (lo + mul128Fold64(i0^(rd64(sec, midsizeStartOffset+32)+seed), i1^(rd64(sec, midsizeStartOffset+40)-seed))) ^ jx
			hi = (hi + mul128Fold64(j0^(rd64(sec, midsizeStartOffset+48)+seed), j1^(rd64(sec, midsizeStartOffset+56)-seed))) ^ ix
			if n >= 224 {
				i0, i1, ix := rd64(in, 192), rd64(in, 200), rd64(in, 192)+rd64(in, 200)
				j0, j1, jx := rd64(in, 208), rd64(in, 216), rd64(in, 208)+rd64(in, 216)
				lo = (lo + mul128Fold64(i0^(rd64(sec, midsizeStartOffset+64)+seed), i1^(rd64(sec, midsizeStartOffset+72)-seed))) ^ jx
				hi = (hi + mul128Fold64(j0^(rd64(sec, midsizeStartOffset+80)+seed), j1^(rd64(sec, midsizeStartOffset+88)-seed))) ^ ix
			}
		}
	}
	// The last round deliberately takes its two chunks in reverse order, and
	// negates the seed.
	i0, i1, ix := rd64(in, n-16), rd64(in, n-8), rd64(in, n-16)+rd64(in, n-8)
	j0, j1, jx := rd64(in, n-32), rd64(in, n-24), rd64(in, n-32)+rd64(in, n-24)
	lo = (lo + mul128Fold64(i0^(rd64(sec, lastRound)-seed), i1^(rd64(sec, lastRound+8)+seed))) ^ jx
	hi = (hi + mul128Fold64(j0^(rd64(sec, lastRound+16)-seed), j1^(rd64(sec, lastRound+24)+seed))) ^ ix
	return finalize128(lo, hi, n, seed)
}

// len129to240_128NS is len129to240_128 without the seed, and spelled
// differently on purpose: its loads sit on lines of their own, each of which
// executes a NOP (see the note on spelling above). Spelled as
// len129to240_128 is, 23 instructions fewer at 162 bytes, it measured 2.6%
// slower over 129..240 bytes on a Zen 4 and about 5% over 160..191, where the
// tail runs one round -- integer scheduler steering again; see sum64NS.
func len129to240_128NS(in unsafe.Pointer, n uintptr, sec unsafe.Pointer) Uint128 {
	lo := uint64(n) * prime64_1
	hi := uint64(0)
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
		lo = (lo + mul128Fold64(i0^rd64(add(sec, lastRound), 0), i1^rd64(add(sec, lastRound), 8))) ^ (j0 + j1)
		hi = (hi + mul128Fold64(j0^rd64(add(sec, lastRound+16), 0), j1^rd64(add(sec, lastRound+16), 8))) ^ (i0 + i1)
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
		// needs 8 data + 8 accumulator registers; amd64 has ~14 to give, and
		// pair-wise halved its spills. A RISC target's 32 hold either.
		//
		// Each word is read on the line that uses it and read again on the
		// other line of its pair; the compiler merges the two reads into one
		// load. Read on a line of their own, the pair's loads left that line
		// with no instruction, and each became an executed NOP: eight a
		// stripe, 7% of this loop on amd64. See the note on spelling above.
		a0 += rd64(in, 8) + mul32(rd64(in, 0)^rd64(sec, 0))
		a1 += rd64(in, 0) + mul32(rd64(in, 8)^rd64(sec, 8))
		a2 += rd64(in, 24) + mul32(rd64(in, 16)^rd64(sec, 16))
		a3 += rd64(in, 16) + mul32(rd64(in, 24)^rd64(sec, 24))
		a4 += rd64(in, 40) + mul32(rd64(in, 32)^rd64(sec, 32))
		a5 += rd64(in, 32) + mul32(rd64(in, 40)^rd64(sec, 40))
		a6 += rd64(in, 56) + mul32(rd64(in, 48)^rd64(sec, 48))
		a7 += rd64(in, 48) + mul32(rd64(in, 56)^rd64(sec, 56))
		// unsafe.Add rather than add: the compiler moves the increments
		// away from these lines, which left add's inline marks as two
		// executed NOPs a stripe.
		in = unsafe.Add(in, stripeLen)
		sec = unsafe.Add(sec, secretConsumeRate)
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
		acc[i] = (acc[i] ^ acc[i]>>47 ^ rd64(sec, 8*i)) * prime32_1
	}
}

// hashLongGeneric consumes the whole input into acc, starting from initAcc:
// whole blocks each followed by a scramble, then the trailing stripes, then
// the final stripe taken from the very end of the input (which may overlap
// the previous one). acc is written, never read; every one-shot hash starts
// from the same state, and the kernels take it from initAcc themselves rather
// than have the caller copy it.
//
// It has the same signature as the assembly backends, and is what dispatch
// falls back to where none of them apply. secretLimit is len(secret)-stripeLen:
// both the scramble key and the final stripe's key are placed relative to it,
// and it is not necessarily a multiple of secretConsumeRate.
func hashLongGeneric(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) {
	*acc = initAcc
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

// hashLongSeedGeneric is the contract of the seeded kernel in portable Go:
// the accumulators of a long input under the secret seed derives, each
// already xored with its word of the two merges' keys -- the derived secret
// at secretMergeAccsStart for keys[0:8] and at secretDefaultSize less a
// stripe less that for keys[8:16]. See sum64SeededLong.
func hashLongSeedGeneric(keys *[2 * accNB]uint64, in unsafe.Pointer, n int, seed uint64) {
	var secret [secretDefaultSize]byte
	deriveSecret(&secret, seed)
	var acc [accNB]uint64
	hashLongGeneric(&acc, in, n, unsafe.Pointer(&secret), secretDefaultSize-stripeLen)
	for i := uintptr(0); i < accNB; i++ {
		keys[i] = acc[i] ^ rd64(unsafe.Pointer(&secret), secretMergeAccsStart+8*i)
		keys[accNB+i] = acc[i] ^ rd64(unsafe.Pointer(&secret), secretDefaultSize-8*accNB-secretMergeAccsStart+8*i)
	}
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
	m := start + mix2Accs(acc, 0, sec)
	m += mix2Accs(acc, 2, add(sec, 16)) + mix2Accs(acc, 4, add(sec, 32))
	return avalanche(m + mix2Accs(acc, 6, add(sec, 48)))
}

// deriveSecret builds the seeded secret used by long inputs. A seeded long hash
// is defined as the unseeded hash under this shifted secret, not as the default
// secret with a seed mixed in.
func deriveSecret(dst *[secretDefaultSize]byte, seed uint64) {
	src := unsafe.Pointer(&kSecret)
	// Work 64 bytes at a time: fixed offsets remove per-word indexing and
	// bounds checks. All three input and output windows fit their arrays.
	for i := uintptr(0); i < secretDefaultSize; i += 64 {
		out := (*[64]byte)(unsafe.Add(unsafe.Pointer(dst), i))
		in := add(src, i)
		binary.LittleEndian.PutUint64(out[0:], rd64(in, 0)+seed)
		binary.LittleEndian.PutUint64(out[8:], rd64(in, 8)-seed)
		binary.LittleEndian.PutUint64(out[16:], rd64(in, 16)+seed)
		binary.LittleEndian.PutUint64(out[24:], rd64(in, 24)-seed)
		binary.LittleEndian.PutUint64(out[32:], rd64(in, 32)+seed)
		binary.LittleEndian.PutUint64(out[40:], rd64(in, 40)-seed)
		binary.LittleEndian.PutUint64(out[48:], rd64(in, 48)+seed)
		binary.LittleEndian.PutUint64(out[56:], rd64(in, 56)-seed)
	}
}
