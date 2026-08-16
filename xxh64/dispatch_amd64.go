//go:build amd64 && !purego

package xxh64

import "unsafe"

// amd64 has one lane loop -- it is bound by the integer multiplier whatever
// the surrounding code does -- but two ways of getting the primes to it, and
// the two x86 vendors want opposite ones.
//
// Holding the five primes in registers, loaded RIP-relative by the prologue,
// is worth 12-16% over 32..256 bytes on an Intel Redwood Cove. The same
// change costs a Zen 3 5-17% over 8..256, fading to nothing by 4 KiB.
// Neither is understood; both reproduce across relinked layouts with
// cespare/xxhash's rows in the same binaries unmoved. So the generator emits
// both forms and this picks between them, the way the arm64 kernel carries
// two lane rounds and picks by core.
//
// The choice is a byte the kernel itself tests, not a branch here: a second
// callee in these wrappers would push the public entry points past the
// inliner's budget and cost every hash a call level, which is more than
// either form is worth. sum64Scalar compares primeForm and jumps to
// sum64ScalarPtr when it is set, so both are still one direct call away and
// the form the caller's CPU wants is the fall-through.
//
// Default is the pointer form -- primeForm nonzero -- because that is what
// shipped before the split and what every non-Intel core has been measured
// on. Only a vendor known to prefer registers turns it off.

// The prime form lives in primes[6], where the kernel reads it as a byte off
// the same cache line it loads the primes from. Zero is the register form,
// nonzero the table pointer.
const (
	formRegister uint64 = 0
	formPointer  uint64 = 1
)

func init() { primes[6] = pickPrimeForm() }

// pickPrimeForm reads the CPUID vendor string. Intel gains from the register
// form; AMD loses by about as much. Anything else gets the pointer form,
// which is the older and more widely measured of the two.
func pickPrimeForm() uint64 {
	max, b, c, d := cpuid(0)
	if max < 1 {
		return formPointer
	}
	// The vendor string is EBX, EDX, ECX in that order: "GenuineIntel" is
	// 0x756e6547 0x49656e69 0x6c65746e.
	if b == 0x756e6547 && d == 0x49656e69 && c == 0x6c65746e {
		return formRegister
	}
	return formPointer
}

//go:noescape
func cpuid(eaxArg uint32) (eax, ebx, ecx, edx uint32)

// These wrappers inline into the public entry points.

func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Scalar(p, n, seed) }

// sum64NS is the unseeded twin: Sum64 and Sum64String reach it in one direct
// call, so an unseeded hash never loads or spends a seed. Which one a caller
// gets is settled at compile time by the entry point it calls, so this costs
// no branch. See TailMaskSkips' neighbour in the generator for the numbers.
func sum64NS(p unsafe.Pointer, n int) uint64 { return sum64ScalarNS(p, n) }

func blocks(v *[4]uint64, p unsafe.Pointer, nb int) { blocksScalar(v, p, nb) }

// Backend names the kernel in use, and which prime form it took.
func Backend() string {
	if primes[6] == formRegister {
		return "scalar"
	}
	return "scalar-ptr"
}

// setBackend forces a prime form, for tests: both are correct on any x86, so
// either can be exercised anywhere.
func setBackend(name string) bool {
	switch name {
	case "scalar":
		primes[6] = formRegister
		return true
	case "scalar-ptr":
		primes[6] = formPointer
		return true
	}
	return false
}
