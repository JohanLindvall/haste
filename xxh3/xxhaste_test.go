package xxh3

import (
	"bytes"
	"encoding"
	"fmt"
	"hash"
	"math/rand"
	"testing"
)

// The reference vectors in vectors_test.go are hashes of a prefix of this
// buffer, which is built exactly as xsum_sanity_check.c builds its own: a
// 64-bit LCG, one byte per step, taken from the top.
func testBuffer(n int) []byte {
	b := make([]byte, n)
	g := uint64(2654435761)
	for i := range b {
		b[i] = byte(g >> 56)
		g *= 11400714785074694797
	}
	return b
}

// testSecret is the custom secret the refSecretVecs were taken under.
func testSecret(n int) []byte {
	b := make([]byte, n)
	g := uint64(0x9E3779B97F4A7C15)
	for i := range b {
		b[i] = byte(g >> 56)
		g = g*11400714785074694797 + 0x2545F4914F6CDD1D
	}
	return b
}

func maxVecLen() int {
	m := 0
	for _, v := range refVecs {
		if v.Len > m {
			m = v.Len
		}
	}
	return m
}

func TestReferenceVectors(t *testing.T) {
	buf := testBuffer(maxVecLen())
	for _, v := range refVecs {
		in := buf[:v.Len]
		var got64 uint64
		var got128 Uint128
		if v.Seed == 0 {
			got64, got128 = Sum64(in), Sum128(in)
		} else {
			got64, got128 = Sum64Seed(in, v.Seed), Sum128Seed(in, v.Seed)
		}
		if got64 != v.H64 {
			t.Errorf("Sum64(len=%d, seed=%#x) = %#016x, want %#016x", v.Len, v.Seed, got64, v.H64)
		}
		if got128.Lo != v.Lo || got128.Hi != v.Hi {
			t.Errorf("Sum128(len=%d, seed=%#x) = {%#016x,%#016x}, want {%#016x,%#016x}",
				v.Len, v.Seed, got128.Lo, got128.Hi, v.Lo, v.Hi)
		}
		// Passing a seed of zero must be the same as passing none, and every
		// string form must agree with its slice form.
		if v.Seed == 0 {
			if Sum64Seed(in, 0) != v.H64 || Sum128Seed(in, 0) != got128 {
				t.Errorf("len=%d: an explicit zero seed changed the hash", v.Len)
			}
			if Sum64String(string(in)) != got64 || Sum128String(string(in)) != got128 {
				t.Errorf("len=%d: string form disagrees with slice form", v.Len)
			}
		} else if Sum64SeedString(string(in), v.Seed) != got64 ||
			Sum128SeedString(string(in), v.Seed) != got128 {
			t.Errorf("len=%d seed=%#x: string form disagrees with slice form", v.Len, v.Seed)
		}
	}
}

func TestReferenceSecretVectors(t *testing.T) {
	maxIn, maxSec := 0, 0
	for _, v := range refSecretVecs {
		if v.Len > maxIn {
			maxIn = v.Len
		}
		if v.SecretLen > maxSec {
			maxSec = v.SecretLen
		}
	}
	buf := testBuffer(maxIn)
	sec := testSecret(maxSec)
	for _, v := range refSecretVecs {
		in, s := buf[:v.Len], sec[:v.SecretLen]
		if got := Sum64Secret(in, s); got != v.H64 {
			t.Errorf("Sum64Secret(len=%d, secretLen=%d) = %#016x, want %#016x",
				v.Len, v.SecretLen, got, v.H64)
		}
		if got := Sum128Secret(in, s); got.Lo != v.Lo || got.Hi != v.Hi {
			t.Errorf("Sum128Secret(len=%d, secretLen=%d) = {%#016x,%#016x}, want {%#016x,%#016x}",
				v.Len, v.SecretLen, got.Lo, got.Hi, v.Lo, v.Hi)
		}
	}
}

// TestStreamingMatchesOneShot drives Write in every awkward chunking it can:
// sizes around the 256-byte staging buffer, around the 64-byte stripe, and
// around the block boundary, plus randomly.
func TestStreamingMatchesOneShot(t *testing.T) {
	buf := testBuffer(4096)
	chunkings := [][]int{
		{1}, {2}, {3}, {7}, {31}, {63}, {64}, {65}, {127}, {128}, {192},
		{255}, {256}, {257}, {512}, {1023}, {1024}, {1025},
		{1, 63}, {63, 1}, {64, 192}, {192, 64}, {255, 1}, {1, 255},
		{100, 156, 1}, {3, 250, 3}, {1024, 1},
	}
	lens := []int{0, 1, 15, 16, 17, 63, 64, 65, 128, 129, 191, 192, 240, 241,
		255, 256, 257, 511, 512, 513, 1023, 1024, 1025, 2048, 4096}

	for _, n := range lens {
		in := buf[:n]
		want64, want128 := Sum64(in), Sum128(in)
		for _, ck := range chunkings {
			d := New()
			for off, i := 0, 0; off < n; i++ {
				k := ck[i%len(ck)]
				if off+k > n {
					k = n - off
				}
				if _, err := d.Write(in[off : off+k]); err != nil {
					t.Fatal(err)
				}
				off += k
			}
			if got := d.Sum64(); got != want64 {
				t.Fatalf("streaming Sum64 len=%d chunks=%v = %#016x, want %#016x", n, ck, got, want64)
			}
			// Reading twice must not disturb the state.
			if got := d.Sum128(); got != want128 {
				t.Fatalf("streaming Sum128 len=%d chunks=%v = %v, want %v", n, ck, got, want128)
			}
			if got := d.Sum64(); got != want64 {
				t.Fatalf("streaming Sum64 (repeat) len=%d chunks=%v changed", n, ck)
			}
		}
	}
}

func TestStreamingRandomChunks(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	buf := testBuffer(1 << 16)
	for trial := 0; trial < 200; trial++ {
		n := rng.Intn(len(buf))
		in := buf[:n]
		d := New()
		for off := 0; off < n; {
			k := 1 + rng.Intn(600)
			if off+k > n {
				k = n - off
			}
			d.Write(in[off : off+k])
			off += k
		}
		if got, want := d.Sum64(), Sum64(in); got != want {
			t.Fatalf("len=%d: streaming %#016x != one-shot %#016x", n, got, want)
		}
		if got, want := d.Sum128(), Sum128(in); got != want {
			t.Fatalf("len=%d: streaming128 %v != one-shot %v", n, got, want)
		}
	}
}

func TestStreamingSeedAndSecret(t *testing.T) {
	buf := testBuffer(3000)
	sec := testSecret(200)
	for _, n := range []int{0, 1, 16, 100, 240, 241, 300, 1024, 1100, 3000} {
		in := buf[:n]
		for _, seed := range []uint64{0, 1, 0x9E3779B185EBCA87} {
			d := NewSeed(seed)
			writeInChunks(d, in, 37)
			if got, want := d.Sum64(), Sum64Seed(in, seed); got != want {
				t.Errorf("seeded stream len=%d seed=%#x: %#016x != %#016x", n, seed, got, want)
			}
			if got, want := d.Sum128(), Sum128Seed(in, seed); got != want {
				t.Errorf("seeded stream128 len=%d seed=%#x: %v != %v", n, seed, got, want)
			}
		}
		d := NewSecret(sec)
		writeInChunks(d, in, 91)
		if got, want := d.Sum64(), Sum64Secret(in, sec); got != want {
			t.Errorf("secret stream len=%d: %#016x != %#016x", n, got, want)
		}
		if got, want := d.Sum128(), Sum128Secret(in, sec); got != want {
			t.Errorf("secret stream128 len=%d: %v != %v", n, got, want)
		}
	}
}

func writeInChunks(d *Digest, in []byte, k int) {
	for off := 0; off < len(in); off += k {
		end := off + k
		if end > len(in) {
			end = len(in)
		}
		d.Write(in[off:end])
	}
}

func TestReset(t *testing.T) {
	buf := testBuffer(2000)
	d := New()
	d.Write(buf)
	d.Reset()
	d.Write(buf[:100])
	if got, want := d.Sum64(), Sum64(buf[:100]); got != want {
		t.Errorf("after Reset: %#016x != %#016x", got, want)
	}
	ds := NewSeed(99)
	ds.Write(buf)
	ds.Reset()
	ds.Write(buf[:500])
	if got, want := ds.Sum64(), Sum64Seed(buf[:500], 99); got != want {
		t.Errorf("seeded after Reset: %#016x != %#016x", got, want)
	}
}

func TestMarshal(t *testing.T) {
	buf := testBuffer(2400)
	for _, n := range []int{0, 5, 100, 256, 300, 1024, 2000} {
		d := New()
		d.Write(buf[:n])
		state, err := d.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		e := New()
		if err := e.UnmarshalBinary(state); err != nil {
			t.Fatal(err)
		}
		// Both must agree now and keep agreeing as more data arrives.
		if d.Sum64() != e.Sum64() {
			t.Fatalf("len=%d: restored digest differs", n)
		}
		d.Write(buf[n : n+300])
		e.Write(buf[n : n+300])
		if got, want := e.Sum64(), d.Sum64(); got != want {
			t.Fatalf("len=%d: restored digest diverged after Write: %#016x != %#016x", n, got, want)
		}
		if got, want := e.Sum64(), Sum64(buf[:n+300]); got != want {
			t.Fatalf("len=%d: restored digest wrong: %#016x != %#016x", n, got, want)
		}
	}
	if err := New().UnmarshalBinary([]byte("nope")); err == nil {
		t.Error("UnmarshalBinary accepted a short state")
	}
	var _ encoding.BinaryMarshaler = New()
	var _ encoding.BinaryUnmarshaler = New()
}

func TestHashInterface(t *testing.T) {
	var h hash.Hash64 = New()
	h.Write([]byte("hello"))
	if h.Size() != 8 || h.BlockSize() != 64 {
		t.Errorf("Size=%d BlockSize=%d", h.Size(), h.BlockSize())
	}
	want := Sum64([]byte("hello"))
	if h.Sum64() != want {
		t.Errorf("Sum64 = %#016x, want %#016x", h.Sum64(), want)
	}
	var big [8]byte
	for i := range big {
		big[i] = byte(want >> (56 - 8*i))
	}
	if got := h.Sum(nil); !bytes.Equal(got, big[:]) {
		t.Errorf("Sum = %x, want %x", got, big)
	}
	if got := h.Sum([]byte("pre")); !bytes.Equal(got, append([]byte("pre"), big[:]...)) {
		t.Errorf("Sum did not append")
	}
}

func TestUint128Bytes(t *testing.T) {
	h := Uint128{Lo: 0x0102030405060708, Hi: 0x1112131415161718}
	want := []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	if got := h.Bytes(); !bytes.Equal(got[:], want) {
		t.Errorf("Bytes = %x, want %x", got, want)
	}
}

// TestShortSecretPanics covers every entry point that takes a secret. Each has
// to check it: a caller who gets the length wrong should find out at the call
// rather than through a hash that is quietly keyed off the end of the slice.
func TestShortSecretPanics(t *testing.T) {
	cases := []struct {
		name string
		fn   func(secret []byte)
	}{
		{"Sum64Secret", func(s []byte) { Sum64Secret([]byte("x"), s) }},
		{"Sum128Secret", func(s []byte) { Sum128Secret([]byte("x"), s) }},
		{"NewSecret", func(s []byte) { NewSecret(s) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, n := range []int{0, 1, MinSecretSize - 1} {
				func() {
					defer func() {
						if recover() == nil {
							t.Errorf("accepted a secret of %d bytes", n)
						}
					}()
					c.fn(make([]byte, n))
				}()
			}
			// The shortest accepted secret must not panic.
			c.fn(make([]byte, MinSecretSize))
		})
	}
}

// TestEmptyAndNilInput pins the zero-length cases together. A nil slice has a
// nil data pointer, so this is also what says that nothing dereferences the
// input before it has looked at the length.
func TestEmptyAndNilInput(t *testing.T) {
	empty := []byte{}
	sec := testSecret(MinSecretSize)

	if Sum64(nil) != Sum64(empty) || Sum64(nil) != Sum64String("") {
		t.Error("Sum64: nil, empty slice and empty string disagree")
	}
	if Sum128(nil) != Sum128(empty) || Sum128(nil) != Sum128String("") {
		t.Error("Sum128: nil, empty slice and empty string disagree")
	}
	if Sum64Seed(nil, 7) != Sum64Seed(empty, 7) || Sum64SeedString("", 7) != Sum64Seed(empty, 7) {
		t.Error("Sum64Seed: nil, empty slice and empty string disagree")
	}
	if Sum128Seed(nil, 7) != Sum128Seed(empty, 7) || Sum128SeedString("", 7) != Sum128Seed(empty, 7) {
		t.Error("Sum128Seed: nil, empty slice and empty string disagree")
	}
	if Sum64Secret(nil, sec) != Sum64Secret(empty, sec) {
		t.Error("Sum64Secret: nil and empty disagree")
	}
	if Sum128Secret(nil, sec) != Sum128Secret(empty, sec) {
		t.Error("Sum128Secret: nil and empty disagree")
	}

	// An unwritten Digest is the empty hash, and empty writes do not move it.
	d := New()
	if got, want := d.Sum64(), Sum64(nil); got != want {
		t.Errorf("unwritten Digest: %#016x != %#016x", got, want)
	}
	for _, p := range [][]byte{nil, {}} {
		if n, err := d.Write(p); n != 0 || err != nil {
			t.Errorf("Write(%v) = %d, %v", p, n, err)
		}
	}
	if n, err := d.WriteString(""); n != 0 || err != nil {
		t.Errorf(`WriteString("") = %d, %v`, n, err)
	}
	if got, want := d.Sum64(), Sum64(nil); got != want {
		t.Errorf("after empty writes: %#016x != %#016x", got, want)
	}

	// An empty write in the middle of a stream must not disturb it either.
	buf := testBuffer(500)
	e := New()
	e.Write(buf[:300])
	e.Write(nil)
	e.WriteString("")
	e.Write(buf[300:])
	if got, want := e.Sum64(), Sum64(buf); got != want {
		t.Errorf("empty write mid-stream: %#016x != %#016x", got, want)
	}
}

// TestNoAlloc guards the property that makes the one-shot entry points usable
// on a hot path: they must not allocate, including the seeded long path, which
// derives a 192-byte secret on the stack.
func TestNoAlloc(t *testing.T) {
	buf := testBuffer(4096)
	sec := testSecret(192)
	cases := []struct {
		name string
		fn   func()
	}{
		{"Sum64/8", func() { sink64 += Sum64(buf[:8]) }},
		{"Sum64/240", func() { sink64 += Sum64(buf[:240]) }},
		{"Sum64/4096", func() { sink64 += Sum64(buf) }},
		{"Sum64Seed/4096", func() { sink64 += Sum64Seed(buf, 7) }},
		{"Sum128/4096", func() { sink64 += Sum128(buf).Lo }},
		{"Sum128Seed/4096", func() { sink64 += Sum128Seed(buf, 7).Lo }},
		{"Sum64Secret/4096", func() { sink64 += Sum64Secret(buf, sec) }},
		{"Sum64String/64", func() { sink64 += Sum64String("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") }},
	}
	for _, c := range cases {
		if n := testing.AllocsPerRun(100, c.fn); n != 0 {
			t.Errorf("%s: %v allocs/op, want 0", c.name, n)
		}
	}
	d := New()
	if n := testing.AllocsPerRun(100, func() { d.Write(buf); sink64 += d.Sum64() }); n != 0 {
		t.Errorf("Digest.Write+Sum64: %v allocs/op, want 0", n)
	}
}

var sink64 uint64

func ExampleSum64() {
	fmt.Printf("%016x\n", Sum64([]byte("xxhaste")))
	// Output: 4fbd86e33e6237c6
}

func ExampleSum128() {
	fmt.Printf("%x\n", Sum128([]byte("xxhaste")).Bytes())
	// Output: a67e18b957d9475c2122a9673d1489fa
}
