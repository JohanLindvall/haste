package xxh3

import (
	"encoding/binary"
	"sync"
	"testing"
)

// Streaming is checked against the one-shot path rather than against vectors of
// its own. That is not a weaker check: the one-shot path is pinned to the C
// implementation by refVecs and refSecretVecs, so agreeing with it means
// agreeing with the reference. What it buys is coverage at lengths and secret
// sizes no vector was generated for.

// TestStreamingDenseLengths walks every length past two fillings of the
// staging buffer, crossing the stripe, the staging area and the block many
// times over. The chunkings are the ones that leave the buffer in an awkward
// state: one byte at a time, a stripe either side, the staging area either
// side, and the whole input at once.
//
// The boundaries are derived from the constants rather than written out,
// because the staging size is a tuning parameter, not wire format: it has
// been 256, 512 and 1024 already, and a literal here quietly stops sitting
// on the edge it was chosen for.
func TestStreamingDenseLengths(t *testing.T) {
	const maxLen = 2*internalBufferSize + 300
	buf := testBuffer(maxLen)
	chunks := []int{1, stripeLen - 1, stripeLen, stripeLen + 1,
		internalBufferSize - 1, internalBufferSize, internalBufferSize + 1, maxLen}

	for n := 0; n <= maxLen; n++ {
		in := buf[:n]
		want64, want128 := Sum64(in), Sum128(in)
		for _, k := range chunks {
			d := New()
			writeInChunks(d, in, k)
			if got := d.Sum64(); got != want64 {
				t.Fatalf("len=%d chunk=%d: Sum64 %#016x != %#016x", n, k, got, want64)
			}
			if got := d.Sum128(); got != want128 {
				t.Fatalf("len=%d chunk=%d: Sum128 %v != %v", n, k, got, want128)
			}
		}
	}
}

// TestStreamingCustomSecretLengths is the streaming counterpart of
// TestReferenceSecretVectors, and the only test that drives accumBlocks with a
// secret whose limit is not a multiple of secretConsumeRate. The reference
// vectors cannot cover it: they are one-shot, so a custom secret reaches the
// kernels through hashLong and never through the streaming walk.
func TestStreamingCustomSecretLengths(t *testing.T) {
	buf := testBuffer(20000)
	lens := []int{0, 1, 240, 241, 256, 512, 1024, 1025, 2048, 4096, 9000, 20000}
	chunks := []int{1, 63, 64, 65, 100, 255, 256, 257, 1024}

	for _, secLen := range kernelSecretLens {
		sec := testSecret(secLen)
		for _, n := range lens {
			in := buf[:n]
			want64, want128 := Sum64Secret(in, sec), Sum128Secret(in, sec)
			for _, k := range chunks {
				d := NewSecret(sec)
				writeInChunks(d, in, k)
				if got := d.Sum64(); got != want64 {
					t.Fatalf("secretLen=%d len=%d chunk=%d: Sum64 %#016x != %#016x",
						secLen, n, k, got, want64)
				}
				if got := d.Sum128(); got != want128 {
					t.Fatalf("secretLen=%d len=%d chunk=%d: Sum128 %v != %v",
						secLen, n, k, got, want128)
				}
			}
		}
	}
}

// TestWriteString pins WriteString to Write. It reaches write by a different
// route -- the string's own bytes, with no copy -- so it is worth checking
// rather than assuming.
func TestWriteString(t *testing.T) {
	buf := testBuffer(1000)
	for _, n := range []int{0, 1, 63, 64, 240, 241, 255, 256, 257, 1000} {
		s := string(buf[:n])
		d, e := New(), New()
		k, err := d.WriteString(s)
		if k != n || err != nil {
			t.Fatalf("WriteString(%d bytes) = %d, %v", n, k, err)
		}
		e.Write(buf[:n])
		if d.Sum64() != e.Sum64() || d.Sum128() != e.Sum128() {
			t.Errorf("len=%d: WriteString differs from Write", n)
		}
	}

	// Interleaved, so the staging buffer is entered both ways within one run.
	d, e := New(), New()
	for off := 0; off+70 <= len(buf); off += 70 {
		if (off/70)%2 == 0 {
			d.WriteString(string(buf[off : off+70]))
		} else {
			d.Write(buf[off : off+70])
		}
		e.Write(buf[off : off+70])
	}
	if d.Sum64() != e.Sum64() {
		t.Error("interleaved WriteString and Write disagree")
	}
}

// TestDigestReadThenWrite covers what the doc comment on Digest claims:
// reading is non-destructive, so a Digest can be read and then written to
// again, any number of times. The interesting part is that a read runs the
// block walk and the final stripe over a *copy* of the state, and a bug there
// shows up only on the write that follows.
func TestDigestReadThenWrite(t *testing.T) {
	buf := testBuffer(4000)
	d := New()
	prev := 0
	for _, end := range []int{0, 1, 64, 240, 241, 256, 1024, 1500, 4000} {
		d.Write(buf[prev:end])
		prev = end
		for pass := 0; pass < 3; pass++ {
			if got, want := d.Sum64(), Sum64(buf[:end]); got != want {
				t.Fatalf("end=%d pass=%d: Sum64 %#016x != %#016x", end, pass, got, want)
			}
			if got, want := d.Sum128(), Sum128(buf[:end]); got != want {
				t.Fatalf("end=%d pass=%d: Sum128 %v != %v", end, pass, got, want)
			}
		}
	}
}

// TestDigestCopy pins the value semantics. A Digest holds no pointers except
// the caller's secret, so copying one forks the stream -- worth stating,
// because a caller who wants that will reach for it.
func TestDigestCopy(t *testing.T) {
	buf := testBuffer(2000)
	for _, at := range []int{0, 100, 256, 700, 1200} {
		d := New()
		d.Write(buf[:at])
		fork := *d

		d.Write(buf[at:1600])
		fork.Write(buf[at:1900])

		if got, want := d.Sum64(), Sum64(buf[:1600]); got != want {
			t.Errorf("at=%d original: %#016x != %#016x", at, got, want)
		}
		if got, want := fork.Sum64(), Sum64(buf[:1900]); got != want {
			t.Errorf("at=%d copy: %#016x != %#016x", at, got, want)
		}
	}
}

// TestMarshalSeedAndSecret covers what TestMarshal does not: a seeded Digest,
// and one built on a custom secret whose limit is not a multiple of
// secretConsumeRate. Neither the seed nor the secret is part of the encoding --
// both come from the Digest being restored into -- so the round trip is only
// defined between matching Digests, which is what is exercised here.
func TestMarshalSeedAndSecret(t *testing.T) {
	buf := testBuffer(3200)
	sec := testSecret(137)
	const seed = 0x9E3779B185EBCA87

	kinds := []struct {
		name string
		make func() *Digest
		sum  func([]byte) uint64
	}{
		{"seeded",
			func() *Digest { return NewSeed(seed) },
			func(b []byte) uint64 { return Sum64Seed(b, seed) }},
		{"secret",
			func() *Digest { return NewSecret(sec) },
			func(b []byte) uint64 { return Sum64Secret(b, sec) }},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			for _, n := range []int{0, 5, 100, 256, 300, 1024, 2700} {
				d := k.make()
				d.Write(buf[:n])
				state, err := d.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				if len(state) != marshaledSize {
					t.Fatalf("len=%d: marshalled %d bytes, want %d", n, len(state), marshaledSize)
				}

				e := k.make()
				if err := e.UnmarshalBinary(state); err != nil {
					t.Fatal(err)
				}
				if d.Sum64() != e.Sum64() {
					t.Fatalf("len=%d: restored Digest differs immediately", n)
				}

				// And it must keep agreeing as the two are driven on together.
				d.Write(buf[n : n+500])
				e.Write(buf[n : n+500])
				if got, want := e.Sum64(), k.sum(buf[:n+500]); got != want {
					t.Fatalf("len=%d: restored Digest wrong: %#016x != %#016x", n, got, want)
				}
			}
		})
	}
}

// TestUnmarshalRejectsBadState checks that a malformed state is refused rather
// than half-restored or panicked on. A Digest is reachable wherever a hash.Hash
// is, so its encoding can arrive from somewhere that is not this package.
func TestUnmarshalRejectsBadState(t *testing.T) {
	d := New()
	d.Write(testBuffer(700))
	good, err := d.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	corrupt := func(off int, v uint32) []byte {
		s := append([]byte(nil), good...)
		binary.LittleEndian.PutUint32(s[len(magic)+off:], v)
		return s
	}
	// The counters are the last 16 bytes of the body, wherever the buffer in
	// front of them ends; deriving that from marshaledSize rather than from the
	// buffer layout keeps this test honest when the layout changes. It did not,
	// once: offsets computed the other way landed inside the buffer and every
	// "corrupt" state below was quietly accepted as valid.
	const (
		lenOff     = marshaledSize - len(magic) - 16
		bufUsedOff = lenOff + 8
		soFarOff   = lenOff + 12
	)

	bad := [][]byte{
		nil,
		{},
		[]byte("nope"),
		[]byte(magic),
		good[:len(good)-1],
		append(append([]byte(nil), good...), 0),
		// bufUsed past the staging buffer, and a value that wraps negative if
		// it is converted to a 32-bit int before being range-checked.
		corrupt(bufUsedOff, internalBufferSize+1),
		corrupt(bufUsedOff, 1<<31),
		corrupt(bufUsedOff, ^uint32(0)),
		// nbStripesSoFar at and past the block length; consumeStripes keeps it
		// strictly below.
		corrupt(soFarOff, uint32((secretDefaultSize-stripeLen)/secretConsumeRate)),
		corrupt(soFarOff, 1<<31),
		corrupt(soFarOff, ^uint32(0)),
	}
	// A wrong magic byte.
	wrongMagic := append([]byte(nil), good...)
	wrongMagic[0] ^= 1
	bad = append(bad, wrongMagic)
	// bufUsed larger than the total length, which no Digest can reach.
	shortTotal := append([]byte(nil), good...)
	binary.LittleEndian.PutUint64(shortTotal[len(magic)+lenOff:], 4)
	bad = append(bad, shortTotal)

	for i, b := range bad {
		e := New()
		e.Write([]byte("prior"))
		if err := e.UnmarshalBinary(b); err == nil {
			t.Errorf("case %d: accepted a bad state", i)
			continue
		}
		// Rejecting must leave the Digest exactly as it was.
		if got, want := e.Sum64(), Sum64([]byte("prior")); got != want {
			t.Errorf("case %d: rejected state disturbed the Digest: %#016x != %#016x", i, got, want)
		}
	}

	// A zero-value Digest has no block length, so there is nothing a state can
	// legitimately be restored into. It must say so rather than divide by it.
	var zero Digest
	if err := zero.UnmarshalBinary(good); err == nil {
		t.Error("UnmarshalBinary accepted a state into a zero-value Digest")
	}
}

// TestConcurrent runs the entry points from several goroutines at once. The
// package writes no shared state after initialization -- the secret and the
// backend selection are both read-only by then -- and this is what says so
// under -race.
func TestConcurrent(t *testing.T) {
	buf := testBuffer(70000)
	lens := []int{0, 1, 17, 64, 240, 241, 1024, 4096, 65536, 70000}

	want64 := make([]uint64, len(lens))
	want128 := make([]Uint128, len(lens))
	for i, n := range lens {
		want64[i], want128[i] = Sum64(buf[:n]), Sum128(buf[:n])
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := New()
			for round := 0; round < 20; round++ {
				for i, n := range lens {
					if Sum64(buf[:n]) != want64[i] || Sum128(buf[:n]) != want128[i] {
						t.Errorf("len=%d: one-shot differs under concurrency", n)
						return
					}
					d.Reset()
					d.Write(buf[:n])
					if d.Sum64() != want64[i] || d.Sum128() != want128[i] {
						t.Errorf("len=%d: Digest differs under concurrency", n)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}
