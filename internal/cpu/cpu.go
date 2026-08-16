// Package cpu identifies the core the process runs on, for the two places
// where a kernel is chosen by core rather than by feature: the parent
// package's split NEON kernel and xxh64's lane-round shape. Both are gated on
// positive identification, so a core this package cannot read stays on the
// portable choice.
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
