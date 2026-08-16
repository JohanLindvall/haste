//go:build arm64 && !purego

package xxh64

import (
	"unsafe"

	"github.com/JohanLindvall/haste/internal/cpu"
)

// arm64 dispatch. The kernel has the lane round in two forms: the fused
// multiply-add, the shape the other Go implementation ships and Neoverse
// cores are known to run at their chain bound, and a separate multiply and
// add, which Apple's cores want -- see internal/asmgen/arm64_xxh64.go. The
// choice lives in the sixth slot of the primes table, written once here at
// package init and read by the kernel itself, only on the path that runs
// the lane loop: callers pass nothing, and a short hash never touches it.
var _ = pickForm()

func pickForm() bool {
	if cpu.Apple() {
		primes[5] = 1
	}
	return true
}

func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Scalar(p, n, seed) }

func blocks(v *[4]uint64, p unsafe.Pointer, nb int) { blocksScalar(v, p, nb) }

// Backend names the lane-round form in use: "madd" or "muladd".
func Backend() string {
	if primes[5] != 0 {
		return "muladd"
	}
	return "madd"
}

// sum64NS is the unseeded entry points' route to the kernel. Only amd64
// generates a twin for it; here it is the seeded kernel with a zero, which is
// what arm64 did before the twin existed.
func sum64NS(p unsafe.Pointer, n int) uint64 { return sum64(p, n, 0) }

// setBackend forces a form, for tests and benchmarks; both run on any arm64
// core.
func setBackend(name string) bool {
	switch name {
	case "madd":
		primes[5] = 0
	case "muladd":
		primes[5] = 1
	default:
		return false
	}
	return true
}
