package xxh64

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"unsafe"
)

func ptr(b []byte) unsafe.Pointer { return unsafe.Pointer(unsafe.SliceData(b)) }

// testBuffer is the canonical input: the same generator as ref/gen.c and the
// parent package, so a vector here can be compared against upstream by hand.
func testBuffer(n int) []byte {
	b := make([]byte, n)
	g := uint64(2654435761)
	for i := range b {
		b[i] = byte(g >> 56)
		g *= 11400714785074694797
	}
	return b
}

// backends is every kernel an assembly build knows about on this
// architecture; the tests run each one this machine can select.
var backends = map[string][]string{
	"amd64": {"scalar"},
	"arm64": {"madd", "muladd"},
}

func candidateBackends() []string {
	if setBackend("generic") {
		return []string{"generic"}
	}
	if names := backends[runtime.GOARCH]; names != nil {
		return names
	}
	return []string{"generic"}
}

// forEachBackend runs f once per selectable backend, restoring the default.
func forEachBackend(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	selected := Backend()
	defer setBackend(selected)
	for _, name := range candidateBackends() {
		if !setBackend(name) {
			t.Fatalf("backend %s not selectable", name)
		}
		t.Run(name, f)
	}
}

func TestVectors(t *testing.T) {
	buf := testBuffer(1 << 21)
	forEachBackend(t, func(t *testing.T) {
		for _, v := range refVecs {
			in := buf[:v.Len]
			var got uint64
			if v.Seed == 0 {
				got = Sum64(in)
			} else {
				got = Sum64Seed(in, v.Seed)
			}
			if got != v.H64 {
				t.Fatalf("len=%d seed=%#x: got %#016x want %#016x", v.Len, v.Seed, got, v.H64)
			}
			if v.Len <= 4096 {
				// The string entry points and the seeded form with seed 0
				// must be the same function.
				if s := Sum64SeedString(string(in), v.Seed); s != v.H64 {
					t.Fatalf("len=%d seed=%#x: string form %#016x", v.Len, v.Seed, s)
				}
				if v.Seed == 0 && (Sum64String(string(in)) != v.H64 || Sum64Seed(in, 0) != v.H64) {
					t.Fatalf("len=%d: unseeded forms disagree", v.Len)
				}
			}
		}
		t.Logf("%d reference vectors reproduced", len(refVecs))
	})
}

// TestStreamingMatchesOneShot writes every length up to a few blocks in
// several chunkings, checking the running hash after every write.
func TestStreamingMatchesOneShot(t *testing.T) {
	buf := testBuffer(700)
	forEachBackend(t, func(t *testing.T) {
		for _, seed := range []uint64{0, 1, 0x9E3779B185EBCA87} {
			for _, chunk := range []int{1, 2, 3, 5, 7, 8, 16, 31, 32, 33, 63, 64, 65, 100, 129, 700} {
				d := NewSeed(seed)
				for off := 0; off < len(buf); off += chunk {
					end := off + chunk
					if end > len(buf) {
						end = len(buf)
					}
					if off%2 == 0 {
						d.Write(buf[off:end])
					} else {
						d.WriteString(string(buf[off:end]))
					}
					if got, want := d.Sum64(), Sum64Seed(buf[:end], seed); got != want {
						t.Fatalf("seed %#x chunk %d at %d: %#016x != %#016x", seed, chunk, end, got, want)
					}
				}
			}
		}
	})
}

func TestStreamingRandomChunks(t *testing.T) {
	buf := testBuffer(5000)
	rng := rand.New(rand.NewSource(1))
	forEachBackend(t, func(t *testing.T) {
		for round := 0; round < 200; round++ {
			d := New()
			off := 0
			for off < len(buf) {
				n := rng.Intn(200)
				if off+n > len(buf) {
					n = len(buf) - off
				}
				d.Write(buf[off : off+n])
				off += n
			}
			if got, want := d.Sum64(), Sum64(buf); got != want {
				t.Fatalf("round %d: %#016x != %#016x", round, got, want)
			}
		}
	})
}

func TestDigestInterface(t *testing.T) {
	d := New()
	if d.Size() != 8 || d.BlockSize() != blockLen {
		t.Fatal("Size/BlockSize")
	}
	buf := testBuffer(100)
	d.Write(buf)
	sum := d.Sum(nil)
	if len(sum) != 8 || binary.BigEndian.Uint64(sum) != Sum64(buf) {
		t.Fatalf("Sum: %x", sum)
	}
	// Sum does not consume; Reset does.
	d.Write(buf)
	if d.Sum64() != Sum64(append(append([]byte{}, buf...), buf...)) {
		t.Fatal("Sum64 after further writes")
	}
	d.Reset()
	if d.Sum64() != Sum64(nil) {
		t.Fatal("Reset")
	}
	// Seeded digests keep their seed across Reset.
	ds := NewSeed(42)
	ds.Write(buf)
	ds.Reset()
	ds.Write(buf)
	if ds.Sum64() != Sum64Seed(buf, 42) {
		t.Fatal("seed lost by Reset")
	}
}

func TestMarshalBinary(t *testing.T) {
	buf := testBuffer(300)
	for _, split := range []int{0, 1, 31, 32, 33, 64, 100, 299, 300} {
		d := NewSeed(7)
		d.Write(buf[:split])
		st, err := d.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		d2 := New()
		if err := d2.UnmarshalBinary(st); err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		d.Write(buf[split:])
		d2.Write(buf[split:])
		if d.Sum64() != d2.Sum64() || d2.Sum64() != Sum64Seed(buf, 7) {
			t.Fatalf("split %d: round trip mismatch", split)
		}
		// A restored digest resets to its own seed.
		d2.Reset()
		d2.Write(buf)
		if d2.Sum64() != Sum64Seed(buf, 7) {
			t.Fatalf("split %d: seed not restored", split)
		}
	}
	// Corrupt states are rejected.
	d := New()
	d.Write(buf[:40])
	st, _ := d.MarshalBinary()
	for _, bad := range [][]byte{nil, st[:len(st)-1], append([]byte("nope"), st[4:]...)} {
		if err := New().UnmarshalBinary(bad); err == nil {
			t.Fatal("bad state accepted")
		}
	}
	// A staged count inconsistent with the total.
	c := append([]byte{}, st...)
	c[len(magic)+40+blockLen] = 5
	if err := New().UnmarshalBinary(c); err == nil {
		t.Fatal("inconsistent state accepted")
	}
}

// TestKernelsMatchPortable calls the linked kernels directly and compares
// them with the portable implementation, which is also what checks the
// portable implementation's own long path.
func TestKernelsMatchPortable(t *testing.T) {
	buf := testBuffer(5000)
	forEachBackend(t, func(t *testing.T) {
		if Backend() == "generic" {
			t.Skip("no kernel")
		}
		for n := 0; n <= 5000; n++ {
			for _, seed := range []uint64{0, 1, ^uint64(0)} {
				p := unsafe.Pointer(&buf[0])
				if got, want := sum64(p, n, seed), sum64Generic(p, n, seed); got != want {
					t.Fatalf("sum64 len %d seed %#x: %#016x != %#016x", n, seed, got, want)
				}
			}
		}
		rng := rand.New(rand.NewSource(2))
		for i := 0; i < 500; i++ {
			var v, w [4]uint64
			for j := range v {
				v[j] = rng.Uint64()
			}
			w = v
			off, nb := rng.Intn(64), rng.Intn(40)
			blocks(&v, unsafe.Pointer(&buf[off]), nb)
			blocksGeneric(&w, unsafe.Pointer(&buf[off]), nb)
			if v != w {
				t.Fatalf("blocks off %d nb %d: %x != %x", off, nb, v, w)
			}
		}
	})
}

func TestBackendName(t *testing.T) {
	names := candidateBackends()
	found := false
	for _, n := range names {
		if n == Backend() {
			found = true
		}
	}
	if !found {
		t.Fatalf("Backend() = %q, not among %v", Backend(), names)
	}
	t.Logf("dispatched to %s", Backend())
}

func ExampleSum64() {
	fmt.Printf("%#016x\n", Sum64([]byte("hello")))
	// Output: 0x26c7827d889f6da3
}
