//go:build amd64 && !purego

package rapidhash

import "unsafe"

// amd64 has two kernels -- seeded and unseeded, chosen by the entry point at
// compile time -- and each carries two forms of the block loop's multiply,
// chosen here at init.
//
// rapidhash's only primitive is a 64x64 multiply. The baseline instruction
// for it is mulq, whose destination is fixed at RDX:RAX. BMI2's mulx names
// both destinations, which frees the register mulq occupies; the block loop
// then holds a lane's secret word there across both of that lane's rounds
// and loads the seven secret words once an iteration instead of twice. That,
// not mulx itself, is where the win is: on a Redwood Cove the pair is worth
// 9-15% from 512 bytes up, and mulx alone about 1%.
//
// The kernel reads the form from secret[9] and tests it after its own n > 112
// branch, so only an input long enough to run the block loop pays for the
// choice and a short hash never executes the test. See x86_rapid.go.
//
// Selection is Intel with BMI2, which is narrower than BMI2 alone on purpose.
// A Zen 4 measured mulx *slower* than mulq per round, and the paired form has
// not been measured on one; until it has, a core that is known to dislike
// half of this change does not get it. Everything else keeps the baseline
// loop, which is what it had before the form existed.

const (
	formMul  uint64 = 0 // baseline mulq
	formMulx uint64 = 1 // BMI2 mulx, with the lane's secret held in rax
)

func init() { secret[9] = pickMulForm() }

// pickMulForm reports whether to take the mulx block loop: BMI2 present
// (CPUID leaf 7, EBX bit 8) and a vendor it has been measured on.
func pickMulForm() uint64 {
	maxID, b, c, d := cpuid(0, 0)
	if maxID < 7 {
		return formMul
	}
	// "GenuineIntel" is EBX, EDX, ECX in that order.
	if b != 0x756e6547 || d != 0x49656e69 || c != 0x6c65746e {
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

// sum64NS is what Sum64 and Sum64String reach: the same hash with the seed
// known to be zero, so the prologue's mix is a constant rather than a
// multiply. The entry point settles which kernel a caller gets at compile
// time, so it costs no branch.
func sum64NS(p unsafe.Pointer, n int) uint64 { return sum64RapidNS(p, n) }

// Backend names the kernel in use, and which multiply form its block loop
// took.
func Backend() string {
	if secret[9] == formMulx {
		return "mulx"
	}
	return "scalar"
}

// setBackend forces a multiply form, for tests and benchmarks. mulx needs
// BMI2 to execute; both forms are correct wherever they run, and the tests
// walk each so that neither goes unexercised on the machine that has it.
func setBackend(name string) bool {
	switch name {
	case "scalar":
		secret[9] = formMul
		return true
	case "mulx":
		if _, ebx, _, _ := cpuid(7, 0); ebx&(1<<8) == 0 {
			return false
		}
		secret[9] = formMulx
		return true
	}
	return false
}

// backendNames lists every kernel this build could dispatch to.
func backendNames() []string { return []string{"scalar", "mulx"} }
