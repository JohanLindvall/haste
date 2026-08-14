package xxhaste

import (
	"runtime"
	"testing"
)

// allBackends is every kernel an assembly build knows about, whether or not
// this machine can run it.
var allBackends = map[string][]string{
	"amd64": {"sse2", "avx2", "avx512"},
	"arm64": {"neon", "sve2-vl128", "sve2-vl256", "sve2-vl512"},
}

// candidateBackends is what this build could dispatch to. A purego build has
// exactly one, whatever the architecture.
func candidateBackends() []string {
	if setBackend("generic") {
		return []string{"generic"}
	}
	if names := allBackends[runtime.GOARCH]; names != nil {
		return names
	}
	return []string{"generic"}
}

// TestBackendsNative runs the whole reference suite again on every backend
// this machine can actually execute, not just the one dispatch picked. The
// simulator in asmsim_test.go covers the rest.
func TestBackendsNative(t *testing.T) {
	selected := Backend()
	t.Logf("%s/%s dispatched to %q", runtime.GOOS, runtime.GOARCH, selected)
	defer setBackend(selected)

	ran := 0
	names := candidateBackends()
	for _, name := range names {
		if !setBackend(name) {
			t.Logf("%s: not available on this machine", name)
			continue
		}
		ran++
		t.Run(name, func(t *testing.T) {
			if Backend() != name {
				t.Fatalf("setBackend(%q) left %q selected", name, Backend())
			}
			TestReferenceVectors(t)
			TestReferenceSecretVectors(t)
			TestStreamingMatchesOneShot(t)
			TestStreamingSeedAndSecret(t)
			TestStreamingRandomChunks(t)
		})
	}
	if ran == 0 {
		t.Fatal("no backend was runnable")
	}
}

// TestBackendsAgree is the cross-check that matters most for the assembly:
// every backend must produce the same bytes as the portable implementation for
// the same input, at every length where the loop structure changes.
func TestBackendsAgree(t *testing.T) {
	selected := Backend()
	defer setBackend(selected)

	buf := testBuffer(1 << 16)
	lens := []int{241, 255, 256, 257, 320, 511, 512, 513, 576, 1023, 1024,
		1025, 1088, 1089, 2048, 4096, 8192, 10000, 65535, 65536}

	// The portable results are the yardstick; take them with the generic
	// path forced on, which purego builds use unconditionally.
	type result struct{ h64, lo, hi uint64 }
	want := map[int]result{}
	for _, n := range lens {
		h := Sum64(buf[:n])
		c := Sum128(buf[:n])
		want[n] = result{h, c.Lo, c.Hi}
	}

	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		for _, n := range lens {
			h, c := Sum64(buf[:n]), Sum128(buf[:n])
			if w := want[n]; h != w.h64 || c.Lo != w.lo || c.Hi != w.hi {
				t.Errorf("%s len=%d: {%#016x,%#016x,%#016x} != {%#016x,%#016x,%#016x}",
					name, n, h, c.Lo, c.Hi, w.h64, w.lo, w.hi)
			}
		}
	}
}
