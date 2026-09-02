package xxh3

import (
	"encoding/binary"
	"errors"
	"hash"
	"unsafe"
)

// Digest computes XXH3 incrementally. The zero value is not usable; obtain one
// from New, NewSeed or NewSecret.
//
// A Digest produces both widths from one pass: Sum64 and Sum128 read the same
// accumulator state and differ only in how they converge it. Calling either is
// non-destructive, so a Digest can be read and then written to again.
type Digest struct {
	// acc is the live accumulator state, updated one stripe at a time.
	acc [accNB]uint64

	// buf is a 64-byte window followed by the staging area:
	//
	//	buf[0:64]              the 64 message bytes immediately preceding the
	//	                       staged ones, once anything has been absorbed
	//	buf[64 : 64+bufUsed]   staged, not yet absorbed
	//
	// Keeping the window ahead of the staging area rather than parking it
	// elsewhere is what makes the last 64 bytes of the message contiguous at
	// buf[bufUsed:], whatever the stripe boundary does, so Sum64 never
	// reassembles anything and Write re-establishes both halves with one copy.
	buf     [stripeLen + internalBufferSize]byte
	bufUsed int

	totalLen uint64

	// nbStripesSoFar counts stripes since the last scramble, so that a block
	// boundary falling inside a Write is still honoured.
	nbStripesSoFar    int
	nbStripesPerBlock int
	secretLimit       int

	// blockMask is nbStripesPerBlock-1 when that is a power of two, which it
	// is for every secret of a standard size, and -1 otherwise. Wrapping the
	// stripe position is on the path of every Write, and a division there is
	// worth a branch to avoid.
	blockMask int

	seed    uint64
	useSeed bool

	// extSecret, when non-nil, is a caller-owned secret used verbatim. It is
	// not copied, so the caller must keep it unmodified for the Digest's life.
	extSecret    []byte
	customSecret [secretDefaultSize]byte
}

var (
	_ hash.Hash64 = (*Digest)(nil)
)

// New returns a Digest computing the default, unseeded XXH3.
func New() *Digest {
	d := &Digest{}
	d.customSecret = kSecret
	d.nbStripesPerBlock = (secretDefaultSize - stripeLen) / secretConsumeRate
	d.secretLimit = secretDefaultSize - stripeLen
	d.setBlockMask()
	d.Reset()
	return d
}

// NewSeed returns a Digest keyed by seed. Unlike Sum64Seed, the per-seed secret
// is derived once here rather than on every hash.
func NewSeed(seed uint64) *Digest {
	d := New()
	if seed != 0 {
		deriveSecret(&d.customSecret, seed)
		d.seed = seed
		d.useSeed = true
	}
	return d
}

// NewSecret returns a Digest keyed by a custom secret. The secret must be at
// least MinSecretSize bytes; see Sum64Secret. It is retained, not copied.
func NewSecret(secret []byte) *Digest {
	checkSecret(secret)
	d := &Digest{}
	d.extSecret = secret
	d.nbStripesPerBlock = (len(secret) - stripeLen) / secretConsumeRate
	d.secretLimit = len(secret) - stripeLen
	d.setBlockMask()
	d.Reset()
	return d
}

func (d *Digest) setBlockMask() {
	d.blockMask = -1
	if n := d.nbStripesPerBlock; n&(n-1) == 0 {
		d.blockMask = n - 1
	}
}

// wrap advances a stripe position within the block.
func (d *Digest) wrap(n int) int {
	if d.blockMask >= 0 {
		return n & d.blockMask
	}
	return n % d.nbStripesPerBlock
}

// Reset restores d to its state just after construction, keeping the seed or
// secret it was built with.
func (d *Digest) Reset() {
	d.acc = initAcc
	d.totalLen = 0
	d.bufUsed = 0
	d.nbStripesSoFar = 0
}

// Size returns 8, the width of Sum64. Use Sum128 for the 128-bit result.
func (d *Digest) Size() int { return 8 }

// BlockSize returns the stripe length, 64 bytes.
func (d *Digest) BlockSize() int { return stripeLen }

func (d *Digest) secretPtr() unsafe.Pointer {
	if d.extSecret != nil {
		return unsafe.Pointer(unsafe.SliceData(d.extSecret))
	}
	return unsafe.Pointer(&d.customSecret)
}

// Write absorbs p. It never fails and never retains p.
func (d *Digest) Write(p []byte) (int, error) {
	n := len(p)
	d.write(p)
	return n, nil
}

// WriteString absorbs s without copying it.
func (d *Digest) WriteString(s string) (int, error) {
	n := len(s)
	d.write(unsafe.Slice(unsafe.StringData(s), n))
	return n, nil
}

// write is nosplit: the fast path below is a few dozen instructions, and
// the stack check was two of them on every Write. Its frame is small and
// what it calls checks its own.
//
//go:nosplit
func (d *Digest) write(p []byte) {
	n := len(p)
	d.totalLen += uint64(n)

	// Nothing is absorbed while everything still fits: a message this short
	// may yet be hashed by the short-input path, which needs all of it, and
	// batching small writes keeps them to one copy each. A write that does
	// not fit either drains the staged whole stripes and is then staged like
	// any other, or is large enough to go through absorb whole.
	//
	// On a Redwood Cove this test is mispredicted every time it is taken at
	// 16- and 64-byte writes -- once per drain, a period the predictor does
	// not learn there -- and each miss takes a return mispredict with it,
	// because the wrong path has popped the return stack by the time the
	// test resolves. That is 7% of a 64-byte write and 3% of a 16-byte one,
	// and none of a 256-byte one, whose period of four it does learn.
	// Inverting the branch by writing the drain as a loop body moved the
	// miss from the taken side to the not-taken side and removed nothing;
	// whether the period is learned turned out to depend on the number of
	// taken branches per write, the caller's included, so no shape here can
	// settle it. Recorded so the next reader does not chase it.
	if d.bufUsed+n > internalBufferSize && d.absorb(p) {
		return
	}
	// The slot is formed by arithmetic rather than by indexing, which
	// would test the index against the array on every write: bufUsed+n has
	// just been held to the staging area and bufUsed is never negative, so
	// the n bytes land inside it.
	dst := add(unsafe.Pointer(&d.buf), uintptr(stripeLen+d.bufUsed))
	src := unsafe.Pointer(unsafe.SliceData(p))
	d.bufUsed += n
	if n > 64 {
		copy(unsafe.Slice((*byte)(dst), n), p)
		return
	}

	// Up to 64 bytes the copy is made here with overlapping fixed-size
	// moves, not with copy: copy is a call into memmove, and for a write of
	// that size the call and memmove's own size dispatch were a third of the
	// cost -- profiled on a Zen 4 at 64-byte writes, memmove was 31% of the
	// cycles where the kernel was 17%. The moves may overlap because p and
	// the staging area never alias. They are 16 bytes at most: the compiler
	// lowers a 16-byte array copy to one SSE move, and a 32-byte one to a
	// call into memmove, which is what this is here to avoid.
	//
	// The trailing moves are addressed from the end of p and of its slot.
	// A 16-byte move takes only a base register and a constant offset, so
	// written as dst+n-16 each one cost two LEAs on top of the move; from
	// the two ends it is one LEA each for all of them and a constant the
	// move absorbs. On a Redwood Cove a 64-byte write spent 39% of its
	// cycles in this function, retiring at the core's width, so its
	// instructions are its cost.
	dstEnd := add(dst, uintptr(n))
	srcEnd := add(src, uintptr(n))
	switch {
	case n > 32:
		*(*[16]byte)(dst) = *(*[16]byte)(src)
		*(*[16]byte)(add(dst, 16)) = *(*[16]byte)(add(src, 16))
		*(*[16]byte)(unsafe.Add(dstEnd, -32)) = *(*[16]byte)(unsafe.Add(srcEnd, -32))
		*(*[16]byte)(unsafe.Add(dstEnd, -16)) = *(*[16]byte)(unsafe.Add(srcEnd, -16))
	case n > 16:
		*(*[16]byte)(dst) = *(*[16]byte)(src)
		*(*[16]byte)(unsafe.Add(dstEnd, -16)) = *(*[16]byte)(unsafe.Add(srcEnd, -16))
	case n > 8:
		*(*[8]byte)(dst) = *(*[8]byte)(src)
		*(*[8]byte)(unsafe.Add(dstEnd, -8)) = *(*[8]byte)(unsafe.Add(srcEnd, -8))
	case n > 4:
		*(*[4]byte)(dst) = *(*[4]byte)(src)
		*(*[4]byte)(unsafe.Add(dstEnd, -4)) = *(*[4]byte)(unsafe.Add(srcEnd, -4))
	case n > 0:
		// 1..4 bytes: the first and the last, then the two in between,
		// which at three bytes are both the middle one. An empty write
		// falls out here, having cost the lengths above nothing.
		*(*byte)(dst) = *(*byte)(src)
		*(*byte)(unsafe.Add(dstEnd, -1)) = *(*byte)(unsafe.Add(srcEnd, -1))
		*(*byte)(add(dst, uintptr(n)>>1)) = *(*byte)(add(src, uintptr(n)>>1))
		*(*byte)(add(dst, uintptr(n)-1-uintptr(n)>>1)) = *(*byte)(add(src, uintptr(n)-1-uintptr(n)>>1))
	}
}

// absorb handles a write that does not fit the staging area, and reports
// whether it took all of p.
//
// A small write drains: the staged whole stripes are absorbed with one
// kernel call and the window and the staged remainder slide down, so that p
// can then be staged by write like any other, and absorb reports false.
// Every staged whole stripe is safe to take, because the write that did not
// fit continues the message past them. That is what a small write pays
// instead of the general path below: on a Zen 4 a kernel call's fixed cost
// (accumulators loaded and stored, prologue) was a third of a 256-byte
// Write, and re-staging p is cheaper until p is most of the staging area,
// which is where the line is drawn. The bound also keeps the slide provably
// in bounds: at most 63 staged bytes remain, so window + remainder + p fits.
//
// A large write takes every stripe that is safe to take from the staged
// bytes followed by p, leaves the window re-established, and absorb reports
// true. A stripe is only safe once the message is known to continue past
// it: the final stripe is absorbed by Sum64, from the end of the message,
// and must not also be absorbed here. That is what the -1 holds back.
//
// Both paths are one function so that the block write jumps to has a call
// in it, which the compiler takes as the unlikely side: staging then falls
// through, and a write pays no taken branch to reach it. The counts below
// are non-negative and the divisions are by a power of two: taken as
// unsigned they are one shift each, where a signed division by a constant
// is four instructions of rounding on every one.
func (d *Digest) absorb(p []byte) bool {
	// The secret and the position within the block are lifted out and the
	// backend is called directly: the wrapper layer around it showed up as
	// 9% of a small-write benchmark.
	sec := d.secretPtr()
	soFar := d.nbStripesSoFar

	if len(p) < internalBufferSize-(stripeLen-1) {
		k := int(uint(d.bufUsed) / stripeLen)
		accumBlocks(&d.acc, unsafe.Pointer(&d.buf[stripeLen]), k, sec, d.secretLimit, soFar)
		d.nbStripesSoFar = d.wrap(soFar + k)
		// Slide the new window -- the last 64 bytes absorbed -- and the
		// staged remainder down.
		rem := int(uint(d.bufUsed) % stripeLen)
		copy(d.buf[:stripeLen+rem], d.buf[k*stripeLen:stripeLen+d.bufUsed])
		d.bufUsed = rem
		return false
	}

	nb := int(uint(d.bufUsed+len(p)-1) / stripeLen)

	// Everything staged, plus enough of p to finish the stripe it ends in.
	// Completing that stripe in place keeps this to one run rather than two,
	// and copies at most 63 bytes instead of filling the staging area.
	staged := int(uint(d.bufUsed) / stripeLen)
	pOff := 0
	if rem := int(uint(d.bufUsed) % stripeLen); rem > 0 && staged < nb {
		pOff = stripeLen - rem
		copy(d.buf[stripeLen+d.bufUsed:], p[:pOff])
		d.bufUsed += pOff
		staged++
	}

	// The staged stripes and then the rest straight out of p, in one call.
	// The two runs are never contiguous, and a write of whole kibibytes
	// stages exactly one stripe -- the one held back from the write before
	// in case it was the message's last -- which in a call of its own cost
	// 123 instructions and 26 cycles on a Redwood Cove for four cycles of
	// work: a fifth of a 1 KiB write. Either count may be zero.
	direct := nb - staged
	accumBlocks2(&d.acc, unsafe.Pointer(&d.buf[stripeLen]), staged, sec, d.secretLimit, soFar,
		add(unsafe.Pointer(unsafe.SliceData(p)), uintptr(pOff)), direct)
	d.nbStripesSoFar = d.wrap(soFar + nb)

	if direct > 0 {
		// The window and what is left over are adjacent at the end of what
		// came out of p, so one copy re-establishes both.
		pOff += direct * stripeLen
		d.bufUsed = copy(d.buf[:], p[pOff-stripeLen:]) - stripeLen
		return true
	}

	// Nothing came out of p, so the window is still inside the staging area.
	// Slide it down with whatever it did not reach, then stage the rest.
	src := stripeLen + staged*stripeLen
	d.bufUsed = copy(d.buf[:], d.buf[src-stripeLen:stripeLen+d.bufUsed]) - stripeLen
	d.bufUsed += copy(d.buf[stripeLen+d.bufUsed:], p[pOff:])
	return true
}

// consumeStripes runs nbStripes stripes through acc, scrambling at each block
// boundary the run crosses, and returns the new position within the block.
// Only Sum64 uses it: absorb calls the backend directly.
func (d *Digest) consumeStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes, soFar int) int {
	accumBlocks(acc, in, nbStripes, d.secretPtr(), d.secretLimit, soFar)
	return d.wrap(soFar + nbStripes)
}

// digestLong finishes a long input on a copy of the accumulators, so that the
// Digest stays usable afterwards.
func (d *Digest) digestLong(acc *[accNB]uint64) {
	*acc = d.acc

	// Whole stripes can only still be staged for a message that never reached
	// the staging capacity; past that, absorb leaves at most a partial one.
	// Where they leave the block position does not matter: only the final
	// stripe follows, and it is keyed from the end of the secret.
	// bufUsed is at least one here: totalLen is past midsizeMax, and absorb
	// never leaves the staging area empty.
	if nb := int(uint(d.bufUsed-1) / stripeLen); nb > 0 {
		d.consumeStripes(acc, unsafe.Pointer(&d.buf[stripeLen]), nb, d.nbStripesSoFar)
	}

	// The final stripe is the last 64 bytes of the message. The window sits
	// directly in front of the staged bytes, so those 64 bytes are contiguous
	// at buf[bufUsed:] wherever the boundary happens to fall.
	accumStripes(acc, add(unsafe.Pointer(&d.buf), uintptr(d.bufUsed)), 1,
		add(d.secretPtr(), uintptr(d.secretLimit-secretLastAccStart)))
}

// Sum64 returns the 64-bit hash of everything written so far.
func (d *Digest) Sum64() uint64 {
	if d.totalLen > midsizeMax {
		var acc [accNB]uint64
		d.digestLong(&acc)
		return mergeAccs(&acc, add(d.secretPtr(), secretMergeAccsStart), d.totalLen*prime64_1)
	}
	if d.useSeed {
		return sum64(unsafe.Pointer(&d.buf[stripeLen]), uintptr(d.totalLen),
			unsafe.Pointer(&kSecret), secretDefaultSize, d.seed)
	}
	return sum64NS(unsafe.Pointer(&d.buf[stripeLen]), uintptr(d.totalLen),
		d.secretPtr(), d.secretLimit+stripeLen)
}

// Sum128 returns the 128-bit hash of everything written so far.
func (d *Digest) Sum128() Uint128 {
	if d.totalLen > midsizeMax {
		var acc [accNB]uint64
		d.digestLong(&acc)
		sec := d.secretPtr()
		return Uint128{
			Lo: mergeAccs(&acc, add(sec, secretMergeAccsStart), d.totalLen*prime64_1),
			Hi: mergeAccs(&acc, add(sec, uintptr(d.secretLimit+stripeLen-8*accNB-secretMergeAccsStart)),
				^(d.totalLen * prime64_2)),
		}
	}
	if d.useSeed {
		return sum128(unsafe.Pointer(&d.buf[stripeLen]), uintptr(d.totalLen),
			unsafe.Pointer(&kSecret), secretDefaultSize, d.seed)
	}
	return sum128NS(unsafe.Pointer(&d.buf[stripeLen]), uintptr(d.totalLen),
		d.secretPtr(), d.secretLimit+stripeLen)
}

// Sum appends the 64-bit hash to b in big-endian order, as hash.Hash requires.
func (d *Digest) Sum(b []byte) []byte {
	h := d.Sum64()
	return append(b, byte(h>>56), byte(h>>48), byte(h>>40), byte(h>>32),
		byte(h>>24), byte(h>>16), byte(h>>8), byte(h))
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

const (
	magic         = "xxh3v1"
	marshaledSize = len(magic) + 8*accNB + stripeLen + internalBufferSize + 8 + 4 + 4
)

// MarshalBinary implements encoding.BinaryMarshaler. What it encodes is the
// accumulator and buffer state; neither the seed nor the secret is part of it,
// the secret because it is the caller's and not owned by the Digest. A state
// is therefore only meaningful when restored into a Digest built the same way,
// which is the contract UnmarshalBinary states.
func (d *Digest) MarshalBinary() ([]byte, error) {
	b := make([]byte, 0, marshaledSize)
	b = append(b, magic...)
	for _, a := range d.acc {
		b = binary.LittleEndian.AppendUint64(b, a)
	}
	b = append(b, d.buf[:]...)
	b = binary.LittleEndian.AppendUint64(b, d.totalLen)
	b = binary.LittleEndian.AppendUint32(b, uint32(d.bufUsed))
	b = binary.LittleEndian.AppendUint32(b, uint32(d.nbStripesSoFar))
	return b, nil
}

var errBadState = errors.New("xxh3: invalid hash state")

// UnmarshalBinary implements encoding.BinaryUnmarshaler. It restores the
// accumulator and buffer state; the seed or secret comes from the Digest being
// unmarshalled into, which must match the one that produced the state.
//
// A state that does not describe a reachable Digest is rejected rather than
// restored, and d is left as it was. The counters are checked before anything
// is written, and as unsigned: bufUsed indexes buf, and a value that wrapped
// negative on a 32-bit int would index it out of range.
//
// A state carries the whole staging buffer, whose size is a tuning parameter
// this package reserves the right to change; a state written by a build with
// a different internalBufferSize fails the length check here rather than
// being misread. It last changed when the staging size went from 512 bytes to
// a block.
func (d *Digest) UnmarshalBinary(b []byte) error {
	if len(b) != marshaledSize || string(b[:len(magic)]) != magic {
		return errBadState
	}
	body := b[len(magic):]
	// The three counters are the last 16 bytes of the body, whatever the
	// buffer in front of them is sized at; deriving the offsets from
	// marshaledSize keeps them right when it changes.
	const (
		lenOff     = marshaledSize - len(magic) - 16
		bufUsedOff = lenOff + 8
		soFarOff   = lenOff + 12
	)
	totalLen := binary.LittleEndian.Uint64(body[lenOff:])
	bufUsed := binary.LittleEndian.Uint32(body[bufUsedOff:])
	soFar := binary.LittleEndian.Uint32(body[soFarOff:])

	// absorb stages at most internalBufferSize bytes and wraps the stripe
	// position strictly below nbStripesPerBlock, and no Digest stages more
	// than it has been given. A zero-value Digest has no block length at all,
	// so a state is rejected rather than wrapped against it.
	if bufUsed > internalBufferSize || uint64(bufUsed) > totalLen ||
		d.nbStripesPerBlock <= 0 || soFar >= uint32(d.nbStripesPerBlock) {
		return errBadState
	}

	for i := range d.acc {
		d.acc[i] = binary.LittleEndian.Uint64(body[8*i:])
	}
	copy(d.buf[:], body[8*accNB:])
	d.totalLen = totalLen
	d.bufUsed = int(bufUsed)
	d.nbStripesSoFar = int(soFar)
	return nil
}
