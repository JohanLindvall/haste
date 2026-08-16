package rapidhash

import (
	"testing"
	"unsafe"
)

// FuzzKernelsAgree holds whatever backend is dispatched to the portable
// implementation, over inputs and seeds the fixed vectors do not cover. On a
// purego build the two sides are the same code, and this degenerates to a
// smoke test.
func FuzzKernelsAgree(f *testing.F) {
	f.Add([]byte(nil), uint64(0))
	f.Add([]byte("rapidhash"), uint64(0))
	f.Add(testBuffer(113), uint64(1))
	f.Add(testBuffer(225), uint64(0x9e3779b185ebca87))
	f.Add(testBuffer(1000), ^uint64(0))

	f.Fuzz(func(t *testing.T, in []byte, seed uint64) {
		want := sum64Generic(unsafe.Pointer(unsafe.SliceData(in)), len(in), seed)
		if got := Sum64Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x: %#016x != portable %#016x", len(in), seed, got, want)
		}
		if got := Sum64SeedString(string(in), seed); got != want {
			t.Fatalf("len=%d seed=%#x: string form differs", len(in), seed)
		}
	})
}

// FuzzPrefixIndependence is the property a kernel that over-reads breaks:
// the hash of n bytes must not depend on anything after them. Copying the
// prefix into an allocation of exactly its length puts whatever follows out
// of reach, and under -race or a sanitizer an over-read becomes a fault
// rather than a wrong answer.
func FuzzPrefixIndependence(f *testing.F) {
	f.Add(testBuffer(300), 17)
	f.Add(testBuffer(1000), 113)
	f.Add(testBuffer(64), 0)

	f.Fuzz(func(t *testing.T, in []byte, cut int) {
		if len(in) == 0 {
			return
		}
		if cut < 0 {
			cut = -cut
		}
		cut %= len(in) + 1

		exact := make([]byte, cut)
		copy(exact, in[:cut])
		if got, want := Sum64(in[:cut]), Sum64(exact); got != want {
			t.Fatalf("len=%d: prefix of a longer buffer %#016x != the bytes alone %#016x",
				cut, got, want)
		}
	})
}
