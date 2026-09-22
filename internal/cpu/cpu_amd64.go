//go:build amd64 && !purego

package cpu

// CPUID returns the registers from the requested leaf and subleaf.
// Callers must check the maximum supported leaf before querying features.
//
//go:noescape
func CPUID(eaxArg, ecxArg uint32) (eax, ebx, ecx, edx uint32)

// XGETBV reads XCR0, which describes the register state enabled by the OS.
// Callers must check CPUID's OSXSAVE bit before using it.
//
//go:noescape
func XGETBV() (eax, edx uint32)
