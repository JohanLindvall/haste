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
// The vendor is coarser than the evidence: only Zen 3 loses, and Zen 4-class
// parts measure the register form level or ahead. Zen 3 and Zen 4 share
// CPUID family 0x19 and differ only in the model, so telling them apart is a
// model list rather than a family check; zen4Model below is that list, on
// the evidence the arm64 MIDR list demands.
//
// Zen 4 is not on the pointer form for a second reason: reaching it is a
// jump. sum64Scalar tests the form byte and, when it is set, jumps to
// sum64ScalarPtr, so the form the list does not name pays a taken branch
// on every hash. Measured on a Ryzen 7 8840HS with both forms forced in
// one binary, over four relinked layouts, the register form as the
// fall-through against the pointer form behind its jump: level to 3.5%
// ahead at 4..32 bytes, 2.6-3.6% at 64, 3.6-4.2% at 256 and 1.5-2.5% at a
// kibibyte, and behind in no layout at any length. That is smaller than
// the 20-30% at 4..16 bytes an earlier pass on this core recorded for the
// same jump (see CLAUDE.md), which four layouts did not reproduce; the
// direction is the same, and it is why the list exists.
//
// The choice is a byte the kernel itself tests, not a branch here: a second
// callee in these wrappers would push the public entry points past the
// inliner's budget and cost every hash a call level, which is more than
// either form is worth. Both forms are still one direct call away and the
// form the caller's CPU wants is the fall-through on the cores listed.
//
// Default is the pointer form -- primeForm nonzero -- because that is what
// shipped before the split and what every non-Intel core has been measured
// on. Only a core known to prefer registers turns it off; Zen 5 and later
// are not on the list until one is measured.

// The prime form lives in primes[6], where the kernel reads it as a byte off
// the same cache line it loads the primes from. Zero is the register form,
// nonzero the table pointer.
const (
	formRegister uint64 = 0
	formPointer  uint64 = 1
)

func init() { primes[6] = pickPrimeForm() }

// pickPrimeForm reads the CPUID vendor string, and on AMD the family and
// model. Intel gains from the register form, and so does Zen 4; Zen 3 loses
// by about as much as Intel gains. Anything else gets the pointer form,
// which is the older and more widely measured of the two.
func pickPrimeForm() uint64 {
	max, b, c, d := cpuid(0)
	if max < 1 {
		return formPointer
	}
	// The vendor string is EBX, EDX, ECX in that order: "GenuineIntel" is
	// 0x756e6547 0x49656e69 0x6c65746e, "AuthenticAMD" 0x68747541 0x69746e65
	// 0x444d4163.
	if b == 0x756e6547 && d == 0x49656e69 && c == 0x6c65746e {
		return formRegister
	}
	if b == 0x68747541 && d == 0x69746e65 && c == 0x444d4163 {
		eax, _, _, _ := cpuid(1)
		if zen4Model(eax) {
			return formRegister
		}
	}
	return formPointer
}

// zen4Model reports whether the family and model in CPUID leaf 1's EAX name
// a Zen 4 core. Family 0x19 is Zen 3 and Zen 4 both; the model separates
// them, and AMD's published ranges are: Zen 4 is 0x10-0x1F (Genoa and Storm
// Peak), 0x60-0x6F (Raphael), 0x70-0x7F (Phoenix, which is the 8840HS the
// numbers above came from) and 0xA0-0xAF (Bergamo and Siena); Zen 3 is
// 0x00-0x0F (Milan), 0x20-0x2F (Vermeer), 0x30-0x3F (Trento), 0x40-0x4F
// (Rembrandt) and 0x50-0x5F (Cezanne). Zen 5 is family 0x1A, and is not
// listed: it has not been measured.
func zen4Model(eax uint32) bool {
	family := (eax >> 8) & 0xf
	model := (eax >> 4) & 0xf
	if family == 0xf {
		// The extended family is bits 27:20, the extended model bits 19:16.
		family += (eax >> 20) & 0xff
		model |= ((eax >> 16) & 0xf) << 4
	}
	if family != 0x19 {
		return false
	}
	switch model >> 4 {
	case 0x1, 0x6, 0x7, 0xa:
		return true
	}
	return false
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
