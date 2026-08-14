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

	// buf stages input until a full 256 bytes is available. It doubles as the
	// holding area for the final partial stripe: see Sum64.
	buf     [internalBufferSize]byte
	bufUsed int

	totalLen uint64

	// nbStripesSoFar counts stripes since the last scramble, so that a block
	// boundary falling inside a Write is still honoured.
	nbStripesSoFar    int
	nbStripesPerBlock int
	secretLimit       int

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
	d.Reset()
	return d
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

	// Common case: everything still fits in the staging buffer.
	if len(p) <= internalBufferSize-d.bufUsed {
		d.bufUsed += copy(d.buf[d.bufUsed:], p)
		return
	}

	// Top up and drain whatever is already staged.
	if d.bufUsed > 0 {
		p = p[copy(d.buf[d.bufUsed:], p):]
		d.consumeStripes(&d.acc, unsafe.Pointer(&d.buf), internalBufferStripes, &d.nbStripesSoFar)
		d.bufUsed = 0
	}

	// Then consume p in place, down to the last whole stripe but never all of
	// it: one byte at minimum is always left for the buffer, which is what
	// lets Sum64 assume it has something staged.
	if len(p) > internalBufferSize {
		nbStripes := (len(p) - 1) / stripeLen
		d.consumeStripes(&d.acc, unsafe.Pointer(unsafe.SliceData(p)), nbStripes, &d.nbStripesSoFar)
		consumed := p[:nbStripes*stripeLen]
		p = p[len(consumed):]
		// Sum64 may need the stripe that just went past, and the remainder of
		// p is too short to contain it. Park it at the end of the buffer,
		// beyond the reach of the copy below.
		copy(d.buf[internalBufferSize-stripeLen:], consumed[len(consumed)-stripeLen:])
	}

	d.bufUsed = copy(d.buf[:], p)
}

// consumeStripes runs nbStripes stripes through acc, scrambling at each block
// boundary. acc and soFar are parameters rather than fields because Sum64 must
// run this over a copy of both.
//
// The whole run is one call: the kernel walks the block boundaries itself, so
// the accumulators stay in registers across them. Where they end up within the
// block follows from the count, so nothing has to come back.
func (d *Digest) consumeStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, soFar *int) {
	if nbStripes == 0 {
		return
	}
	accumBlocks(acc, in, nbStripes, d.secretPtr(), d.secretLimit, *soFar)
	*soFar = (*soFar + nbStripes) % d.nbStripesPerBlock
}

// digestLong finishes a long input on a copy of the accumulators, so that the
// Digest stays usable afterwards.
func (d *Digest) digestLong(acc *[accNB]uint64) {
	*acc = d.acc
	lastSec := add(d.secretPtr(), uintptr(d.secretLimit-secretLastAccStart))

	if d.bufUsed >= stripeLen {
		soFar := d.nbStripesSoFar
		d.consumeStripes(acc, unsafe.Pointer(&d.buf), (d.bufUsed-1)/stripeLen, &soFar)
		accumStripes(acc, add(unsafe.Pointer(&d.buf), uintptr(d.bufUsed-stripeLen)), 1, lastSec)
	} else {
		// Fewer than 64 bytes are staged, so the final stripe straddles the
		// end of the previously consumed data and what is buffered now. The
		// tail was parked at the end of buf by write.
		var last [stripeLen]byte
		catchup := stripeLen - d.bufUsed
		copy(last[:catchup], d.buf[internalBufferSize-catchup:])
		copy(last[catchup:], d.buf[:d.bufUsed])
		accumStripes(acc, unsafe.Pointer(&last), 1, lastSec)
	}
}

// Sum64 returns the 64-bit hash of everything written so far.
func (d *Digest) Sum64() uint64 {
	if d.totalLen > midsizeMax {
		var acc [accNB]uint64
		d.digestLong(&acc)
		return mergeAccs(&acc, add(d.secretPtr(), secretMergeAccsStart), d.totalLen*prime64_1)
	}
	if d.useSeed {
		return sum64(unsafe.Pointer(&d.buf), uintptr(d.totalLen),
			unsafe.Pointer(&kSecret), secretDefaultSize, d.seed)
	}
	return sum64(unsafe.Pointer(&d.buf), uintptr(d.totalLen),
		d.secretPtr(), d.secretLimit+stripeLen, 0)
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
		return sum128(unsafe.Pointer(&d.buf), uintptr(d.totalLen),
			unsafe.Pointer(&kSecret), secretDefaultSize, d.seed)
	}
	return sum128(unsafe.Pointer(&d.buf), uintptr(d.totalLen),
		d.secretPtr(), d.secretLimit+stripeLen, 0)
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
	marshaledSize = len(magic) + 8*accNB + internalBufferSize + 8 + 4 + 4
)

// MarshalBinary implements encoding.BinaryMarshaler. The state of a Digest
// built with NewSecret cannot be encoded, because the secret is not owned by
// the Digest; unmarshalling into such a Digest is still allowed.
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
func (d *Digest) UnmarshalBinary(b []byte) error {
	if len(b) != marshaledSize || string(b[:len(magic)]) != magic {
		return errBadState
	}
	b = b[len(magic):]
	for i := range d.acc {
		d.acc[i] = binary.LittleEndian.Uint64(b[8*i:])
	}
	b = b[8*accNB:]
	copy(d.buf[:], b)
	b = b[internalBufferSize:]
	d.totalLen = binary.LittleEndian.Uint64(b)
	d.bufUsed = int(binary.LittleEndian.Uint32(b[8:]))
	d.nbStripesSoFar = int(binary.LittleEndian.Uint32(b[12:]))
	if d.bufUsed > internalBufferSize || d.nbStripesSoFar > d.nbStripesPerBlock {
		return errBadState
	}
	return nil
}
