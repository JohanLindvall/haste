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

func hashLong(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) {
	switch backend {
	case backendSVE2VL256:
		hashLongSVE2VL256(acc, in, n, sec, secretLimit)
	case backendSVE2VL512:
		hashLongSVE2VL512(acc, in, n, sec, secretLimit)
	case backendSVE2VL128:
		hashLongSVE2VL128(acc, in, n, sec, secretLimit)
	case backendNEONHybrid:
		hashLongNEONHybrid(acc, in, n, sec, secretLimit)
	case backendNEONHybrid2:
		hashLongNEONHybrid2(acc, in, n, sec, secretLimit)
	default:
		hashLongNEON(acc, in, n, sec, secretLimit)
	}
}

func accumStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer) {
	switch backend {
	case backendSVE2VL256:
		accumSVE2VL256(acc, in, nbStripes, sec)
	case backendSVE2VL512:
		accumSVE2VL512(acc, in, nbStripes, sec)
	case backendSVE2VL128:
		accumSVE2VL128(acc, in, nbStripes, sec)
	case backendNEONHybrid:
		accumNEONHybrid(acc, in, nbStripes, sec)
	case backendNEONHybrid2:
		accumNEONHybrid2(acc, in, nbStripes, sec)
	default:
		accumNEON(acc, in, nbStripes, sec)
	}
}

func accumBlocks(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int) {
	switch backend {
	case backendSVE2VL256:
		accumBlocksSVE2VL256(acc, in, nbStripes, sec, secretLimit, soFar)
	case backendSVE2VL512:
		accumBlocksSVE2VL512(acc, in, nbStripes, sec, secretLimit, soFar)
	case backendSVE2VL128:
		accumBlocksSVE2VL128(acc, in, nbStripes, sec, secretLimit, soFar)
	case backendNEONHybrid:
		accumBlocksNEONHybrid(acc, in, nbStripes, sec, secretLimit, soFar)
	case backendNEONHybrid2:
		accumBlocksNEONHybrid2(acc, in, nbStripes, sec, secretLimit, soFar)
	default:
		accumBlocksNEON(acc, in, nbStripes, sec, secretLimit, soFar)
	}
}
