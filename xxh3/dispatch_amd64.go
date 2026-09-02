//go:build amd64 && !purego

package xxh3

import "unsafe"

// amd64 dispatch.
//
// SSE2 is part of the amd64 baseline, so it needs no detection and there is no
// scalar fallback. AVX2 and AVX-512 are selected only when the CPU reports
// them and the operating system has enabled the corresponding register state:
// a CPU can have AVX-512 while the kernel refuses to save ZMM registers, and
// using it then corrupts other processes' state.

type backendID uint8

const (
	backendSSE2 backendID = iota
	backendAVX2
	backendAVX512
)

var backendNames = map[backendID]string{
	backendSSE2:   "sse2",
	backendAVX2:   "avx2",
	backendAVX512: "avx512",
}

// dispatch_amd64.s compares backend against these values by number; an
// index that is not a constant zero refuses to compile if they ever move.
var (
	_ = [1]struct{}{}[backendSSE2]
	_ = [1]struct{}{}[backendAVX2-1]
	_ = [1]struct{}{}[backendAVX512-2]
)

var backend = pickBackend()

func pickBackend() backendID {
	maxID, _, _, _ := cpuid(0, 0)
	if maxID < 7 {
		return backendSSE2
	}
	// OSXSAVE says XGETBV is usable at all; without it nothing below applies.
	_, _, ecx1, _ := cpuid(1, 0)
	if ecx1&(1<<27) == 0 {
		return backendSSE2
	}
	xcr0, _ := xgetbv()
	const (
		xmmState = 1 << 1
		ymmState = 1 << 2
		opmask   = 1 << 5
		zmmHi256 = 1 << 6
		hi16ZMM  = 1 << 7
	)
	if xcr0&(xmmState|ymmState) != xmmState|ymmState {
		return backendSSE2
	}
	_, ebx7, _, _ := cpuid(7, 0)
	const (
		featAVX2     = 1 << 5
		featAVX512F  = 1 << 16
		featAVX512DQ = 1 << 17
		// The scramble's 64-bit lane multiply is VPMULLQ, which is DQ rather
		// than F. Everything since Skylake-X has both; Knights Landing has
		// only F, and lands on AVX2 here.
		featAVX512 = featAVX512F | featAVX512DQ
	)
	if ebx7&featAVX512 == featAVX512 && xcr0&(opmask|zmmHi256|hi16ZMM) == opmask|zmmHi256|hi16ZMM {
		return backendAVX512
	}
	if ebx7&featAVX2 != 0 {
		return backendAVX2
	}
	return backendSSE2
}

// Backend names the kernel selected for this machine.
func Backend() string { return backendNames[backend] }

// setBackend forces a backend, for tests and benchmarks. It reports whether
// the name is one this machine can actually run.
func setBackend(name string) bool {
	for id, n := range backendNames {
		if n != name {
			continue
		}
		if id > pickBackend() {
			return false
		}
		backend = id
		return true
	}
	return false
}

// The three entry points are assembly, in dispatch_amd64.s: each reads
// backend and jumps to the kernel it names. A Go switch here was a real call
// between sum64NS and the kernel -- three cases cost 199 nodes against the
// inliner's budget of 80 -- so a long hash reached its kernel two calls deep,
// and the streaming path went through a function variable to avoid the same
// switch, at the price of an indirect call and of every argument escaping.
// A tail jump between two ABI0 functions with the same frame costs a byte
// load, a compare and a taken branch, and is one call from wherever it is
// made.

//go:noescape
func hashLong(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)

//go:noescape
func accumStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer)

//go:noescape
func accumBlocks(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int)

//go:noescape
func cpuid(eaxArg, ecxArg uint32) (eax, ebx, ecx, edx uint32)

//go:noescape
func xgetbv() (eax, edx uint32)
