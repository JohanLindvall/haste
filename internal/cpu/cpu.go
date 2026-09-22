// Package cpu provides CPU probes for the hash dispatchers: CPUID and XCR0
// on amd64, and core identification on arm64. Dispatchers keep their own
// selection policies so that a feature is used only where it is profitable.
package cpu

// Implementer and Part decode a MIDR_EL1 value: implementer is bits 31:24,
// part number bits 15:4.
func Implementer(midr uint64) uint32 { return uint32(midr >> 24 & 0xff) }
func Part(midr uint64) uint32        { return uint32(midr >> 4 & 0xfff) }

// Implementer codes.
const (
	ImplementerArm   = 0x41
	ImplementerApple = 0x61
)

// Apple reports whether the core is one of Apple's. On macOS every arm64
// core is; on Linux the answer comes from MIDR_EL1, and is false wherever
// that cannot be read.
func Apple() bool {
	if appleOS {
		return true
	}
	v, ok := MIDR()
	return ok && Implementer(v) == ImplementerApple
}
