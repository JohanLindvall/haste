package xxh64

import (
	"encoding/binary"
	"errors"
	"hash"
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

func (d *Digest) write(p unsafe.Pointer, n int) {
	d.total += uint64(n)
	if d.n+n < blockLen {
		copy(d.buf[d.n:], unsafe.Slice((*byte)(p), n))
		d.n += n
		return
	}
	// i walks the input; p is only ever offset by it while bytes remain,
	// because a pointer past the end of the input is not one checkptr allows
	// forming.
	i := 0
	if d.n > 0 {
		// Complete the staged block first.
		i = blockLen - d.n
		copy(d.buf[d.n:], unsafe.Slice((*byte)(p), i))
		blocks(&d.v, unsafe.Pointer(&d.buf), 1)
		d.n = 0
	}
	if nb := (n - i) / blockLen; nb > 0 {
		blocks(&d.v, add(p, i), nb)
		i += nb * blockLen
	}
	if i < n {
		copy(d.buf[:], unsafe.Slice((*byte)(add(p, i)), n-i))
	}
	d.n = n - i
}

// Sum64 returns the hash of everything written so far. It does not change the
// state; more can be written afterwards.
func (d *Digest) Sum64() uint64 {
	var h uint64
	if d.total >= blockLen {
		v := d.v
		h = mergeLanes(&v)
	} else {
		h = d.seed + prime5
	}
	return finalize(h+d.total, unsafe.Pointer(&d.buf), d.n)
}

// Sum appends the big-endian hash to b, as [hash.Hash] specifies.
func (d *Digest) Sum(b []byte) []byte {
	return binary.BigEndian.AppendUint64(b, d.Sum64())
}

const (
	magic         = "xxh64v1"
	marshaledSize = len(magic) + 8*4 + 8 + blockLen + 1
)

// MarshalBinary implements [encoding.BinaryMarshaler]: the lanes, the byte
// count, the staged bytes and their number, and the seed is not among them --
// it is recoverable from nothing else, so it travels in the lanes' initial
// values, which is enough because Reset is the only thing that needs it and
// a restored Digest is reset by restoring it again.
func (d *Digest) MarshalBinary() ([]byte, error) {
	b := make([]byte, 0, marshaledSize+8)
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
	if len(b) != marshaledSize+8 || string(b[:len(magic)]) != magic {
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
	if n >= blockLen || (total < blockLen && uint64(n) != total) || (total >= blockLen && (total-uint64(n))%blockLen != 0) {
		return errBadState
	}
	d.v = v
	d.total = total
	copy(d.buf[:], body[40:40+blockLen])
	d.n = n
	d.seed = seed
	return nil
}
