package xxh64

import (
	"testing"
	"unsafe"
)

// As in the parent package: the fixed vectors pin a few hundred lengths of
// one buffer under four seeds, and these targets state the properties that
// must hold for any input. Their seed corpora run under `go test`; finding
// new ones needs an explicit -fuzz run.

func writeInChunks(d *Digest, in []byte, k int) {
	for off := 0; off < len(in); off += k {
		end := off + k
		if end > len(in) {
			end = len(in)
		}
		d.Write(in[off:end])
	}
}

// FuzzStreamingMatchesOneShot is the property the streaming state machine
// rests on, with the chunk size chosen by the fuzzer.
func FuzzStreamingMatchesOneShot(f *testing.F) {
	f.Add([]byte("xxhaste"), uint64(0), uint8(0))
	f.Add(testBuffer(300), uint64(1), uint8(0))
	f.Add(testBuffer(1000), uint64(0), uint8(31))
	f.Add(testBuffer(2000), uint64(0x9E3779B185EBCA87), uint8(254))
	f.Add(testBuffer(5000), uint64(^uint64(0)), uint8(191))

	f.Fuzz(func(t *testing.T, in []byte, seed uint64, chunk uint8) {
		k := int(chunk) + 1
		d := NewSeed(seed)
		writeInChunks(d, in, k)
		if got, want := d.Sum64(), Sum64Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x chunk=%d: %#016x != %#016x", len(in), seed, k, got, want)
		}
		d.Reset()
		writeInChunks(d, in, k)
		if got, want := d.Sum64(), Sum64Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x chunk=%d: after Reset %#016x != %#016x", len(in), seed, k, got, want)
		}
		// Marshal at the split and continue on the copy.
		d.Reset()
		half := len(in) / 2
		d.Write(in[:half])
		st, err := d.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		d2 := New()
		if err := d2.UnmarshalBinary(st); err != nil {
			t.Fatal(err)
		}
		d2.Write(in[half:])
		if got, want := d2.Sum64(), Sum64Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x: after marshal %#016x != %#016x", len(in), seed, got, want)
		}
	})
}

// FuzzKernelsAgree compares every kernel this machine can run, and the
// portable implementation, on the same input.
func FuzzKernelsAgree(f *testing.F) {
	f.Add(testBuffer(32), uint64(0))
	f.Add(testBuffer(33), uint64(1))
	f.Add(testBuffer(1000), uint64(0x9E3779B185EBCA87))
	f.Add(testBuffer(4097), uint64(^uint64(0)))

	f.Fuzz(func(t *testing.T, in []byte, seed uint64) {
		want := sum64Generic(unsafe.Pointer(unsafe.SliceData(in)), len(in), seed)
		selected := Backend()
		defer setBackend(selected)
		for _, name := range candidateBackends() {
			if !setBackend(name) {
				continue
			}
			if got := Sum64Seed(in, seed); got != want {
				t.Fatalf("%s: len=%d seed=%#x: %#016x != portable %#016x", name, len(in), seed, got, want)
			}
		}
	})
}

// FuzzUnmarshalBinary makes sure no state, however mangled, is accepted and
// then read out of bounds or hashed inconsistently.
func FuzzUnmarshalBinary(f *testing.F) {
	d := New()
	d.Write(testBuffer(100))
	st, _ := d.MarshalBinary()
	f.Add(st)
	f.Add(st[:10])
	f.Fuzz(func(t *testing.T, b []byte) {
		d := New()
		if err := d.UnmarshalBinary(b); err != nil {
			return
		}
		// Whatever was accepted must round-trip and hash without panicking.
		d.Write(testBuffer(50))
		d.Sum64()
		st2, err := d.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := New().UnmarshalBinary(st2); err != nil {
			t.Fatalf("re-marshalled state rejected: %v", err)
		}
	})
}
