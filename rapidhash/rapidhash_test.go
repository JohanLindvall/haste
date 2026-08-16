package rapidhash

import (
	"fmt"
	"math/rand"
	"testing"
	"unsafe"
)

func ptr(b []byte) unsafe.Pointer { return unsafe.Pointer(unsafe.SliceData(b)) }

// testBuffer is filled exactly as ref/rapidgen.c fills its own, so a length
// here holds the bytes the vector at that length was taken over.
func testBuffer(n int) []byte {
	b := make([]byte, n)
	g := uint64(2654435761)
	for i := range b {
		b[i] = byte(g >> 56)
		g *= 11400714785074694797
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

// candidateBackends is what this build could dispatch to. A purego build has
// exactly one, whatever the architecture.
func candidateBackends() []string {
	if setBackend("generic") {
		return []string{"generic"}
	}
	return backendNames()
}

// TestVectors is the check everything else rests on: the results the C
// implementation produced, reproduced on every backend this machine can run.
func TestVectors(t *testing.T) {
	selected := Backend()
	defer setBackend(selected)

	buf := testBuffer(maxVecLen())
	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, v := range refVecs {
				in := buf[:v.Len]
				if got := Sum64Seed(in, v.Seed); got != v.H64 {
					t.Fatalf("len=%d seed=%#x: %#016x, want %#016x", v.Len, v.Seed, got, v.H64)
				}
				if v.Seed == 0 {
					if got := Sum64(in); got != v.H64 {
						t.Errorf("len=%d: unseeded %#016x, want %#016x", v.Len, got, v.H64)
					}
					if got := Sum64String(string(in)); got != v.H64 {
						t.Errorf("len=%d: string form disagrees", v.Len)
					}
				} else if got := Sum64SeedString(string(in), v.Seed); got != v.H64 {
					t.Errorf("len=%d seed=%#x: seeded string form disagrees", v.Len, v.Seed)
				}
			}
			t.Logf("%d reference vectors reproduced", len(refVecs))
		})
	}
}

// TestKernelsMatchPortable runs the assembly that is linked into this binary
// against the portable implementation, at every length that changes a path
// and a spread beyond. On a purego build the two sides are the same code and
// this proves only that the harness works.
func TestKernelsMatchPortable(t *testing.T) {
	selected := Backend()
	defer setBackend(selected)

	buf := testBuffer(2048)
	var lens []int
	for n := 0; n <= 512; n++ {
		lens = append(lens, n)
	}
	lens = append(lens, 671, 672, 673, 895, 896, 897, 1024, 1337, 2048)

	seeds := []uint64{0, 1, 0x9e3779b185ebca87, ^uint64(0)}

	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, n := range lens {
				for _, seed := range seeds {
					want := sum64Generic(ptr(buf), n, seed)
					if got := Sum64Seed(buf[:n], seed); got != want {
						t.Fatalf("len=%d seed=%#x: kernel %#016x != portable %#016x",
							n, seed, got, want)
					}
				}
			}
		})
	}
}

// TestEmptyAndNil pins the zero-length cases together. A nil slice has a nil
// data pointer, so this is also what says nothing dereferences the input
// before it has looked at the length.
func TestEmptyAndNil(t *testing.T) {
	if Sum64(nil) != Sum64([]byte{}) || Sum64(nil) != Sum64String("") {
		t.Error("nil, empty slice and empty string disagree")
	}
	if Sum64Seed(nil, 7) != Sum64Seed([]byte{}, 7) || Sum64SeedString("", 7) != Sum64Seed(nil, 7) {
		t.Error("seeded: nil, empty slice and empty string disagree")
	}
}

// TestSeedZeroIsUnseeded pins the documented equivalence.
func TestSeedZeroIsUnseeded(t *testing.T) {
	buf := testBuffer(600)
	for _, n := range []int{0, 1, 3, 4, 8, 16, 17, 112, 113, 224, 225, 600} {
		if Sum64Seed(buf[:n], 0) != Sum64(buf[:n]) {
			t.Errorf("len=%d: an explicit zero seed changed the hash", n)
		}
	}
}

// TestSubSliceIndependence catches a kernel that reads past its length: the
// same bytes must hash the same whether or not more follow them.
func TestSubSliceIndependence(t *testing.T) {
	big := testBuffer(4096)
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 500; trial++ {
		n := rng.Intn(len(big))
		exact := append([]byte(nil), big[:n]...)
		if got, want := Sum64(big[:n]), Sum64(exact); got != want {
			t.Fatalf("len=%d: reading a prefix differs from the bytes alone: %#016x != %#016x",
				n, got, want)
		}
	}
}

// TestNoAlloc guards what makes the entry points usable on a hot path.
func TestNoAlloc(t *testing.T) {
	buf := testBuffer(4096)
	cases := []struct {
		name string
		fn   func()
	}{
		{"Sum64/8", func() { sink += Sum64(buf[:8]) }},
		{"Sum64/100", func() { sink += Sum64(buf[:100]) }},
		{"Sum64/4096", func() { sink += Sum64(buf) }},
		{"Sum64Seed/4096", func() { sink += Sum64Seed(buf, 7) }},
		{"Sum64String/16", func() { sink += Sum64String("0123456789abcdef") }},
	}
	for _, c := range cases {
		if n := testing.AllocsPerRun(100, c.fn); n != 0 {
			t.Errorf("%s: %v allocs/op, want 0", c.name, n)
		}
	}
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
	fmt.Printf("%#016x\n", Sum64([]byte("rapidhash")))
	// Output: 0xe58461f8a9f8311c
}
