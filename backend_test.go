package xxhaste

import (
	"runtime"
	"testing"
	"unsafe"
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
			TestStreamingCustomSecretLengths(t)
			TestStreamingDenseLengths(t)
		})
	}
	if ran == 0 {
		t.Fatal("no backend was runnable")
	}
}

// TestBackendsAgree checks that every backend produces the same bytes at every
// length where the loop structure changes. The yardstick is whichever backend
// is selected first, not the portable implementation: setBackend cannot reach
// the portable path from an assembly build, because there is nothing to
// dispatch to it. TestKernelsMatchPortable is the test that compares the two.
func TestBackendsAgree(t *testing.T) {
	selected := Backend()
	defer setBackend(selected)

	buf := testBuffer(1 << 16)
	lens := []int{241, 255, 256, 257, 320, 511, 512, 513, 576, 1023, 1024,
		1025, 1088, 1089, 2048, 4096, 8192, 10000, 65535, 65536}

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

// kernelSecretLens are secret lengths that move the block boundary. 137, 191
// and 193 are the ones that matter: their secretLimit is not a multiple of
// secretConsumeRate, so a kernel that placed the scramble key at
// nbStripesPerBlock*secretConsumeRate instead of at secretLimit would diverge
// only here.
var kernelSecretLens = []int{136, 137, 144, 191, 192, 193, 200, 256, 1024}

// TestKernelsMatchPortable calls the three kernel entry points as they are
// linked into this binary and compares each with the portable implementation
// in the same process.
//
// Neither of the other two backend tests covers this. TestSimulatedBackends
// executes the generator's instruction stream through the simulator, not the
// bytes in the .s file. TestBackendsNative executes those bytes, but reaches
// them only through the public API, and the reference vectors behind it are
// one-shot: a custom secret there enters through hashLong. accumBlocks under a
// secret whose limit is not a multiple of secretConsumeRate has no vector at
// all, and this is the only place it runs.
//
// Comparing against generic.go is enough because everything above these three
// functions -- the ladders, the convergence, the streaming state machine -- is
// architecture-independent Go that every backend shares.
func TestKernelsMatchPortable(t *testing.T) {
	selected := Backend()
	defer setBackend(selected)

	buf := testBuffer(1 << 16)
	bp := unsafe.Pointer(&buf[0])

	for _, name := range candidateBackends() {
		if !setBackend(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, secLen := range kernelSecretLens {
				sec := testSecret(secLen)
				sp := unsafe.Pointer(&sec[0])
				limit := secLen - stripeLen
				perBlock := limit / secretConsumeRate
				block := perBlock * stripeLen

				// hashLong: the first length that reaches the long path, the
				// stripe and block boundaries either side, and inputs needing
				// several blocks.
				for _, n := range []int{241, 256, 257, 512, 1023, 1024, 1025,
					block - 1, block, block + 1, block + stripeLen,
					2*block + stripeLen + 1, 9000, 40000} {
					if n < 241 || n > len(buf) {
						continue
					}
					want, got := initAcc, initAcc
					hashLongGeneric(&want, bp, n, sp, limit)
					hashLong(&got, bp, n, sp, limit)
					if got != want {
						t.Fatalf("hashLong len=%d secretLen=%d:\n got %v\nwant %v",
							n, secLen, got, want)
					}
				}

				// accumBlocks: from every starting position in the block, with
				// runs that stop short of the next boundary, land exactly on
				// it, and cross one or more.
				for _, soFar := range []int{0, 1, perBlock / 2, perBlock - 1} {
					for _, stripes := range []int{1, 2, perBlock - soFar - 1,
						perBlock - soFar, perBlock - soFar + 1, perBlock,
						2*perBlock + 3, 3 * perBlock} {
						if stripes <= 0 || stripes*stripeLen > len(buf) {
							continue
						}
						want, got := initAcc, initAcc
						accumBlocksGeneric(&want, bp, stripes, sp, limit, soFar)
						accumBlocks(&got, bp, stripes, sp, limit, soFar)
						if got != want {
							t.Fatalf("accumBlocks secretLen=%d soFar=%d stripes=%d:\n got %v\nwant %v",
								secLen, soFar, stripes, got, want)
						}
					}
				}

				// accumStripes: one run against one secret position. The count
				// is capped by the secret, which has to hold a whole stripe at
				// the last position the run reaches.
				for _, stripes := range []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 16, 17, 33} {
					if stripes > 0 && (stripes-1)*secretConsumeRate+stripeLen > secLen {
						continue
					}
					want, got := initAcc, initAcc
					accumulateGeneric(&want, bp, sp, stripes)
					accumStripes(&got, bp, stripes, sp)
					if got != want {
						t.Fatalf("accumStripes secretLen=%d stripes=%d:\n got %v\nwant %v",
							secLen, stripes, got, want)
					}
				}
			}
		})
	}
}
