package xxhaste

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

func (d *Digest) write(p []byte) {
	if len(p) == 0 {
		return
	}
	d.totalLen += uint64(len(p))

	// Nothing is absorbed while everything still fits: a message this short
	// may yet be hashed by the short-input path, which needs all of it, and
	// batching small writes keeps them to one copy each.
	if d.bufUsed+len(p) <= internalBufferSize {
		d.bufUsed += copy(d.buf[stripeLen+d.bufUsed:], p)
		return
	}
	d.absorb(p)
}

// absorb takes every stripe that is safe to take from the staged bytes
// followed by p, and leaves the window re-established.
//
// A stripe is only safe once the message is known to continue past it: the
// final stripe is absorbed by Sum64, from the end of the message, and must not
// also be absorbed here. That is what the -1 holds back.
func (d *Digest) absorb(p []byte) {
	// The secret and the position within the block are lifted out and the
	// backend is called directly: this runs twice per Write, and the wrapper
	// layer around it showed up as 9% of a small-write benchmark.
	sec := d.secretPtr()
	soFar := d.nbStripesSoFar

	// A small write drains with one kernel call: the staged whole stripes
	// are absorbed -- all safe, since p continues the message -- and p goes
	// back to being staged. The general path below makes two calls, one for
	// the staged bytes and one straight out of p, and on a Zen 4 the call's
	// fixed cost (accumulators loaded and stored, prologue) was a third of a
	// 256-byte Write; the extra copy of p here is cheaper until p is most of
	// the staging area. The bound also keeps the slide provably in bounds:
	// at most 63 staged bytes remain, so window + remainder + p fits.
	if len(p) < internalBufferSize-(stripeLen-1) {
		k := d.bufUsed / stripeLen
		accumBlocksStream(&d.acc, unsafe.Pointer(&d.buf[stripeLen]), k, sec, d.secretLimit, soFar)
		d.nbStripesSoFar = d.wrap(soFar + k)
		// Slide the new window -- the last 64 bytes absorbed -- and the
		// staged remainder down, then stage p after them.
		rem := d.bufUsed - k*stripeLen
		copy(d.buf[:stripeLen+rem], d.buf[k*stripeLen:stripeLen+d.bufUsed])
		d.bufUsed = rem + copy(d.buf[stripeLen+rem:], p)
		return
	}

	nb := (d.bufUsed + len(p) - 1) / stripeLen

	// Everything staged, plus enough of p to finish the stripe it ends in.
	// Completing that stripe in place keeps this to one run rather than two,
	// and copies at most 63 bytes instead of filling the staging area.
	staged := d.bufUsed / stripeLen
	pOff := 0
	if rem := d.bufUsed - staged*stripeLen; rem > 0 && staged < nb {
		pOff = stripeLen - rem
		copy(d.buf[stripeLen+d.bufUsed:], p[:pOff])
		d.bufUsed += pOff
		staged++
	}
	if staged > 0 {
		accumBlocksStream(&d.acc, unsafe.Pointer(&d.buf[stripeLen]), staged, sec, d.secretLimit, soFar)
		soFar = d.wrap(soFar + staged)
		nb -= staged
	}

	if nb > 0 {
		// The rest comes straight out of p. The window and what is left over
		// are adjacent at its end, so one copy re-establishes both.
		accumBlocksStream(&d.acc, unsafe.Pointer(&p[pOff]), nb, sec, d.secretLimit, soFar)
		d.nbStripesSoFar = d.wrap(soFar + nb)
		pOff += nb * stripeLen
		d.bufUsed = copy(d.buf[:], p[pOff-stripeLen:]) - stripeLen
		return
	}
	d.nbStripesSoFar = soFar

	// Nothing came out of p, so the window is still inside the staging area.
	// Slide it down with whatever it did not reach, then stage the rest.
	src := stripeLen + staged*stripeLen
	d.bufUsed = copy(d.buf[:], d.buf[src-stripeLen:stripeLen+d.bufUsed]) - stripeLen
	d.bufUsed += copy(d.buf[stripeLen+d.bufUsed:], p[pOff:])
}

// consumeStripes runs nbStripes stripes through acc, scrambling at each block
// boundary the run crosses, and returns the new position within the block.
// Only Sum64 uses it: absorb calls the backend directly. It goes through the
// dispatch switch, not accumBlocksStream: a call through a function variable
// makes its arguments escape, and acc here is digestLong's stack copy.
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
	if nb := (d.bufUsed - 1) / stripeLen; nb > 0 {
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

var errBadState = errors.New("xxhaste: invalid hash state")

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
