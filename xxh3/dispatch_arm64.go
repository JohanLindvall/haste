//go:build arm64 && !purego

package xxh3

import "unsafe"

// arm64 dispatch.
//
// NEON is not optional on arm64, so it is the baseline rather than an upgrade:
// there is no scalar fallback to select. SVE2 is chosen only when the hardware
// has it and its vector length matches one of the generated kernels, because a
// stripe is a fixed 64 bytes and how many registers that occupies is exactly
// what SVE leaves unspecified.

// sveVectorLength reads the hardware vector length in bytes. It must not be
// called unless hasSVE2 reported true: the instruction faults otherwise.
func sveVectorLength() int

type backendID uint8

const (
	backendNEON backendID = iota
	backendNEONHybrid
	backendNEONHybrid2
	backendSVE2VL128
	backendSVE2VL256
	backendSVE2VL512
)

var backendNames = map[backendID]string{
	backendNEON:        "neon",
	backendNEONHybrid:  "neon-hybrid",
	backendNEONHybrid2: "neon-hybrid2",
	backendSVE2VL128:   "sve2-vl128",
	backendSVE2VL256:   "sve2-vl256",
	backendSVE2VL512:   "sve2-vl512",
}

// dispatch_arm64.s compares backend against these values by number; an
// index that is not a constant zero refuses to compile if they ever move.
var (
	_ = [1]struct{}{}[backendNEON]
	_ = [1]struct{}{}[backendNEONHybrid-1]
	_ = [1]struct{}{}[backendNEONHybrid2-2]
	_ = [1]struct{}{}[backendSVE2VL128-3]
	_ = [1]struct{}{}[backendSVE2VL256-4]
	_ = [1]struct{}{}[backendSVE2VL512-5]
)

var backend = pickBackend()

func pickBackend() backendID {
	if hybridCore {
		// Splitting the stripe across the vector and integer pipes beats
		// anything else on the cores this is enabled for; see cpu_linux_arm64.go.
		return backendNEONHybrid
	}
	if !hasSVE2() {
		return backendNEON
	}
	switch sveVectorLength() {
	case 16:
		// At 128 bits SVE2 has no width advantage over NEON and needs one
		// more instruction per stripe to step the secret, so NEON wins.
		return backendNEON
	case 32:
		return backendSVE2VL256
	case 64:
		return backendSVE2VL512
	}
	return backendNEON
}

// Backend names the kernel selected for this machine.
func Backend() string { return backendNames[backend] }

// backendVL is the vector length in bytes each SVE2 kernel is built for.
var backendVL = map[backendID]int{
	backendSVE2VL128: 16,
	backendSVE2VL256: 32,
	backendSVE2VL512: 64,
}

// setBackend forces a backend, for tests and benchmarks. It reports whether
// the name is one this machine can actually run.
func setBackend(name string) bool {
	for id, n := range backendNames {
		if n != name {
			continue
		}
		if vl, isSVE := backendVL[id]; isSVE {
			if !hasSVE2() || sveVectorLength() != vl {
				return false
			}
		}
		backend = id
		return true
	}
	return false
}

// The four entry points are assembly, in dispatch_arm64.s: each reads
// backend and jumps to the kernel it names, as the amd64 ones do. A Go
// switch here was a real call between sum64NS and the kernel -- six cases
// are far past the inliner's budget -- with a frame, a stack check and the
// eight arguments spilled for the ABI0 call inside it: 22 instructions on
// the way in and out, on a core that retires four a cycle. A tail jump
// between two ABI0 functions with the same frame costs a byte load, a
// compare and a taken branch, and is one call from wherever it is made.
// Measured on a Neoverse N2, see CLAUDE.md.

//go:noescape
func hashLong(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)

//go:noescape
func accumStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer)

//go:noescape
func accumBlocks(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int)

//go:noescape
func accumBlocks2(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int, in2 unsafe.Pointer, nbStripes2 int)
