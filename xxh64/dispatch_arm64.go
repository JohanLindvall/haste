//go:build arm64 && !purego

package xxh64

import (
	"unsafe"

	"github.com/JohanLindvall/xxhaste/internal/cpu"
)

// arm64 dispatch. The kernel has the lane round in two forms and takes the
// choice as its last argument: zero for the fused multiply-add, the shape
// the other Go implementation ships and Neoverse cores are known to run at
// their chain bound; one for a separate multiply and add, which Apple's
// cores want -- see internal/asmgen/arm64_xxh64.go. The choice is a variable
// read by the wrappers below, which inline into their callers and reach the
// kernel in one direct call.

// split is 1 for the split lane round, 0 for the fused one.
var split = pickForm()

func pickForm() int {
	if cpu.Apple() {
		return 1
	}
	return 0
}

func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Scalar(p, n, seed, split) }

func blocks(v *[4]uint64, p unsafe.Pointer, nb int) { blocksScalar(v, p, nb, split) }

// Backend names the lane-round form in use: "madd" or "muladd".
func Backend() string {
	if split != 0 {
		return "muladd"
	}
	return "madd"
}

// setBackend forces a form, for tests and benchmarks; both run on any arm64
// core.
func setBackend(name string) bool {
	switch name {
	case "madd":
		split = 0
	case "muladd":
		split = 1
	default:
		return false
	}
	return true
}
