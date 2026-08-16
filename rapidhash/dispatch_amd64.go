//go:build amd64 && !purego

package rapidhash

import "unsafe"

// amd64 has one kernel, carrying two forms of the block loop's multiply.
//
// rapidhash's only primitive is a 64x64 multiply. On x86 the baseline
// instruction for it is mulq, whose destination is fixed at RDX:RAX, so every
// round spends a move getting the result out. BMI2's mulx names both
// destinations and costs one instruction fewer per round -- six against seven
// -- in a loop that is bound by how many instructions it is.
//
// mulx is not in the amd64 baseline, so the form is chosen here and read by
// the kernel out of secret[8]. The kernel tests it after its own n > 112
// branch, so only an input long enough to reach the block loop pays for the
// choice; a short hash never executes the test. See x86_rapid.go.

const (
	formMul  uint64 = 0 // baseline mulq
	formMulx uint64 = 1 // BMI2 mulx
)

func init() { secret[8] = pickMulForm() }

// pickMulForm reports whether this machine has BMI2, which is CPUID leaf 7's
// EBX bit 8.
func pickMulForm() uint64 {
	if maxID, _, _, _ := cpuid(0, 0); maxID < 7 {
		return formMul
	}
	if _, ebx, _, _ := cpuid(7, 0); ebx&(1<<8) != 0 {
		return formMulx
	}
	return formMul
}

//go:noescape
func cpuid(eaxArg, ecxArg uint32) (eax, ebx, ecx, edx uint32)

// sum64 hashes n bytes at p under seed in one call into the kernel, whatever
// n is. It inlines into the public entry points, so a hash costs one call.
func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Rapid(p, n, seed) }

// Backend names the kernel in use, and which multiply form its block loop
// took.
func Backend() string {
	if secret[8] == formMulx {
		return "mulx"
	}
	return "scalar"
}

// setBackend forces a multiply form, for tests and benchmarks. Only a machine
// with BMI2 can select mulx; both are correct everywhere else.
func setBackend(name string) bool {
	switch name {
	case "scalar":
		secret[8] = formMul
		return true
	case "mulx":
		if pickMulForm() != formMulx {
			return false
		}
		secret[8] = formMulx
		return true
	}
	return false
}

// backendNames lists every kernel this build could dispatch to.
func backendNames() []string { return []string{"scalar", "mulx"} }
