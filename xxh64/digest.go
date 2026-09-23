package xxh64

import (
	"encoding/binary"
	"errors"
	"hash"
	"math/bits"
	"unsafe"
)

// Digest is a streaming XXH64. It implements [hash.Hash64],
// [encoding.BinaryMarshaler] and [encoding.BinaryUnmarshaler]; the zero value
// is not usable, use [New] or [NewSeed].
//
// Its state is the four lanes, the byte count, and up to a block of input not
// yet absorbed. Every whole block Write can reach goes straight from the
// caller's slice to the kernel; only the pieces that do not fill a block are
// staged.
type Digest struct {
	v     [4]uint64
	total uint64
	buf   [blockLen]byte
	n     int // bytes staged in buf
	seed  uint64
}

var _ hash.Hash64 = (*Digest)(nil)

// New returns a Digest computing the unseeded XXH64.
func New() *Digest { return NewSeed(0) }

// NewSeed returns a Digest computing XXH64 under seed.
func NewSeed(seed uint64) *Digest {
	d := &Digest{seed: seed}
	d.Reset()
	return d
}

// Reset returns the Digest to its initial state, keeping its seed.
func (d *Digest) Reset() {
	d.v = initLanes(d.seed)
	d.total = 0
	d.n = 0
}

// Size returns the hash length in bytes: 8.
func (d *Digest) Size() int { return 8 }

// BlockSize returns the input block length: 32.
func (d *Digest) BlockSize() int { return blockLen }

// Write absorbs b. It never fails.
func (d *Digest) Write(b []byte) (int, error) {
	d.write(unsafe.Pointer(unsafe.SliceData(b)), len(b))
	return len(b), nil
}

// WriteString absorbs s without copying it.
func (d *Digest) WriteString(s string) (int, error) {
	d.write(unsafe.Pointer(unsafe.StringData(s)), len(s))
	return len(s), nil
}

// write stages partial blocks with bounded head/tail moves. The private
// staging buffer does not alias the caller's input.
// The small staging frame needs no stack check; callees check their own stacks.
//
//go:nosplit
func (d *Digest) write(p unsafe.Pointer, n int) {
	d.total += uint64(n)
	if n < blockLen-d.n {
		dst := unsafe.Add(unsafe.Pointer(&d.buf), d.n)
		src := p
		d.n += n
		// Written out rather than calling copySmall, which is past the
		// inliner's budget: this is the path every small write takes.
		switch {
		case n >= 16:
			*(*[16]byte)(dst) = *(*[16]byte)(src)
			*(*[16]byte)(unsafe.Add(dst, n-16)) = *(*[16]byte)(unsafe.Add(src, n-16))
		case n >= 8:
			*(*[8]byte)(dst) = *(*[8]byte)(src)
			*(*[8]byte)(unsafe.Add(dst, n-8)) = *(*[8]byte)(unsafe.Add(src, n-8))
		case n >= 4:
			*(*[4]byte)(dst) = *(*[4]byte)(src)
			*(*[4]byte)(unsafe.Add(dst, n-4)) = *(*[4]byte)(unsafe.Add(src, n-4))
		case n > 0:
			*(*byte)(dst) = *(*byte)(src)
			*(*byte)(unsafe.Add(dst, n>>1)) = *(*byte)(unsafe.Add(src, n>>1))
			*(*byte)(unsafe.Add(dst, n-1)) = *(*byte)(unsafe.Add(src, n-1))
		}
		return
	}
	// i walks the input; p is only ever offset by it while bytes remain,
	// because a pointer past the end of the input is not one checkptr allows
	// forming.
	i := 0
	if d.n > 0 {
		if completeInGo {
			i = d.complete(p)
		} else {
			// Complete the staged block first.
			i = blockLen - d.n
			copy(d.buf[d.n:], unsafe.Slice((*byte)(p), i))
			blocks(&d.v, unsafe.Pointer(&d.buf), 1)
			d.n = 0
		}
	}
	// The remaining length is non-negative. An unsigned division avoids
	// the signed quotient's rounding instructions on the block path.
	if nb := int(uint(n-i) / blockLen); nb > 0 {
		blocks(&d.v, add(p, i), nb)
		i += nb * blockLen
	}
	if i < n {
		if completeInGo {
			copySmall(unsafe.Pointer(&d.buf), add(p, i), n-i)
		} else {
			copy(d.buf[:], unsafe.Slice((*byte)(add(p, i)), n-i))
		}
	}
	d.n = n - i
}

// complete fills the staged block from p and absorbs it, and returns how
// many bytes of p that took: at most 31, copied with fixed moves rather
// than a call into memmove, and one block, absorbed right here rather than
// through a kernel call that loads and stores the lanes and the primes
// around it. write takes it where completeInGo says so. It is a leaf of its own so
// that a write with nothing staged -- every write of whole blocks -- passes
// it by. Measured on a Neoverse N2 against the memmove and the kernel call,
// streaming 64 KiB: 8-byte writes 12% faster, 16-byte 17-22% and 31-byte
// 18%, and 64-byte to kibibyte writes level. A first reading had 256-byte
// writes 7% slower; three benchmark callers with different frames put the
// two builds level there, the old one having drawn the fast frame.
func (d *Digest) complete(p unsafe.Pointer) int {
	n := blockLen - d.n
	dst := unsafe.Add(unsafe.Pointer(&d.buf), d.n)
	switch {
	case n >= 16:
		*(*[16]byte)(dst) = *(*[16]byte)(p)
		*(*[16]byte)(unsafe.Add(dst, n-16)) = *(*[16]byte)(unsafe.Add(p, n-16))
	case n >= 8:
		*(*[8]byte)(dst) = *(*[8]byte)(p)
		*(*[8]byte)(unsafe.Add(dst, n-8)) = *(*[8]byte)(unsafe.Add(p, n-8))
	case n >= 4:
		*(*[4]byte)(dst) = *(*[4]byte)(p)
		*(*[4]byte)(unsafe.Add(dst, n-4)) = *(*[4]byte)(unsafe.Add(p, n-4))
	default:
		*(*byte)(dst) = *(*byte)(p)
		*(*byte)(unsafe.Add(dst, n>>1)) = *(*byte)(unsafe.Add(p, n>>1))
		*(*byte)(unsafe.Add(dst, n-1)) = *(*byte)(unsafe.Add(p, n-1))
	}
	b := unsafe.Pointer(&d.buf)
	p1, p2 := kPrime1, kPrime2
	d.v[0] = bits.RotateLeft64(d.v[0]+rd64(b, 0)*p2, 31) * p1
	d.v[1] = bits.RotateLeft64(d.v[1]+rd64(b, 8)*p2, 31) * p1
	d.v[2] = bits.RotateLeft64(d.v[2]+rd64(b, 16)*p2, 31) * p1
	d.v[3] = bits.RotateLeft64(d.v[3]+rd64(b, 24)*p2, 31) * p1
	d.n = 0
	return n
}

// copySmall copies n < 32 bytes from src to dst, which do not overlap, with
// overlapping head and tail moves: without a call into memmove, which for
// a copy this short was most of what it cost. Byte arrays keep the moves
// legal on every architecture whatever the alignment, and none is wider
// than 16 bytes, which is where amd64 would turn one back into a call.
func copySmall(dst, src unsafe.Pointer, n int) {
	switch {
	case n >= 16:
		*(*[16]byte)(dst) = *(*[16]byte)(src)
		*(*[16]byte)(unsafe.Add(dst, n-16)) = *(*[16]byte)(unsafe.Add(src, n-16))
	case n >= 8:
		*(*[8]byte)(dst) = *(*[8]byte)(src)
		*(*[8]byte)(unsafe.Add(dst, n-8)) = *(*[8]byte)(unsafe.Add(src, n-8))
	case n >= 4:
		*(*[4]byte)(dst) = *(*[4]byte)(src)
		*(*[4]byte)(unsafe.Add(dst, n-4)) = *(*[4]byte)(unsafe.Add(src, n-4))
	case n > 0:
		*(*byte)(dst) = *(*byte)(src)
		*(*byte)(unsafe.Add(dst, n>>1)) = *(*byte)(unsafe.Add(src, n>>1))
		*(*byte)(unsafe.Add(dst, n-1)) = *(*byte)(unsafe.Add(src, n-1))
	}
}

// Sum64 returns the hash of everything written so far. It does not change the
// state; more can be written afterwards.
func (d *Digest) Sum64() uint64 {
	var h uint64
	if d.total >= blockLen {
		h = mergeLanes(&d.v) + d.total
		if d.n == 0 {
			return avalanche(h)
		}
	} else {
		h = d.seed + kPrime5 + d.total
	}
	return finalize(h, unsafe.Pointer(&d.buf), d.n)
}

// Sum appends the big-endian hash to b, as [hash.Hash] specifies.
func (d *Digest) Sum(b []byte) []byte {
	return binary.BigEndian.AppendUint64(b, d.Sum64())
}

const (
	magic         = "xxh64v1"
	marshaledSize = len(magic) + 8*4 + 8 + blockLen + 1 + 8
)

// MarshalBinary implements [encoding.BinaryMarshaler]. It encodes the lanes,
// byte count, staging buffer and its used length, and seed. A restored Digest
// keeps its seed across Reset.
func (d *Digest) MarshalBinary() ([]byte, error) {
	b := make([]byte, 0, marshaledSize)
	b = append(b, magic...)
	for _, v := range d.v {
		b = binary.LittleEndian.AppendUint64(b, v)
	}
	b = binary.LittleEndian.AppendUint64(b, d.total)
	b = append(b, d.buf[:]...)
	b = append(b, byte(d.n))
	b = binary.LittleEndian.AppendUint64(b, d.seed)
	return b, nil
}

var errBadState = errors.New("xxh64: invalid hash state")

// UnmarshalBinary implements [encoding.BinaryUnmarshaler]. It accepts only
// what MarshalBinary produced.
func (d *Digest) UnmarshalBinary(b []byte) error {
	if len(b) != marshaledSize || string(b[:len(magic)]) != magic {
		return errBadState
	}
	body := b[len(magic):]
	var v [4]uint64
	for i := range v {
		v[i] = binary.LittleEndian.Uint64(body[8*i:])
	}
	total := binary.LittleEndian.Uint64(body[32:])
	n := int(body[40+blockLen])
	seed := binary.LittleEndian.Uint64(body[41+blockLen:])
	if uint64(n) != total%blockLen {
		return errBadState
	}
	d.v = v
	d.total = total
	copy(d.buf[:], body[40:40+blockLen])
	d.n = n
	d.seed = seed
	return nil
}
