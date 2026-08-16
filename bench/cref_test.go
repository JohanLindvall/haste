//go:build cgo

package bench

import (
	"testing"

	"github.com/JohanLindvall/xxhaste/xxh3"
	"github.com/JohanLindvall/xxhaste/xxh64"
)

// TestSameAsC closes the loop the vectors open: not just vectors taken from
// the C implementation once, but the C implementation itself, compiled into
// the test binary and asked directly, at every length where any path
// changes, seeded and not, one-shot and streamed.
func TestSameAsC(t *testing.T) {
	if cXXH3 == nil {
		t.Fatal("cgo build without the C hooks; init failed")
	}
	buf := buffer(1 << 16)
	for _, n := range []int{0, 1, 3, 4, 8, 9, 16, 17, 31, 32, 33, 63, 64, 65,
		100, 128, 129, 240, 241, 256, 511, 512, 1024, 1025, 4096, 65535, 65536} {
		in := buf[:n]
		if got, want := xxh3.Sum64(in), cXXH3(in); got != want {
			t.Errorf("len=%d: Sum64 %#016x != C %#016x", n, got, want)
		}
		if got, want := xxh3.Sum64Seed(in, 42), cXXH3Seed(in, 42); got != want {
			t.Errorf("len=%d: Sum64Seed %#016x != C %#016x", n, got, want)
		}
		lo, hi := cXXH3128(in)
		if got := xxh3.Sum128(in); got.Lo != lo || got.Hi != hi {
			t.Errorf("len=%d: Sum128 {%#x,%#x} != C {%#x,%#x}", n, got.Lo, got.Hi, lo, hi)
		}
		if got, want := xxh64.Sum64(in), cXXH64(in, 0); got != want {
			t.Errorf("len=%d: xxh64.Sum64 %#016x != C %#016x", n, got, want)
		}
		if got, want := xxh64.Sum64Seed(in, 42), cXXH64(in, 42); got != want {
			t.Errorf("len=%d: xxh64.Sum64Seed %#016x != C %#016x", n, got, want)
		}
		// The C streaming helpers against the one-shots, at a chunk size that
		// leaves every kind of remainder.
		if got, want := cXXH3Stream(in, 7), cXXH3(in); got != want {
			t.Errorf("len=%d: C stream %#016x != C one-shot %#016x", n, got, want)
		}
		if got, want := cXXH64Stream(in, 7), cXXH64(in, 0); got != want {
			t.Errorf("len=%d: C xxh64 stream %#016x != C one-shot %#016x", n, got, want)
		}
	}
}
