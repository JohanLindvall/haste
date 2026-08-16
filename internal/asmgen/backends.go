package asmgen

import "fmt"

// Backend is one generated kernel family.
//
// The unroll factors are not uniform. On x86 four stripes per iteration keeps
// the loop overhead well under the vector work at every width. On SVE the
// limit is the addressing mode: loads are offset in multiples of the vector
// length with a reach of eight, so a 128-bit kernel, which spends four
// registers per stripe, can only unroll twice.
type Backend struct {
	Name   string // file and label name
	Suffix string // Go identifier suffix
	GOARCH string
	// VL is the SVE vector length in bytes this kernel is built for, or 0.
	VL  int
	New func() Arch
}

// Backends lists everything the generator produces.
func Backends() []Backend {
	return []Backend{
		{Name: "sse2", Suffix: "SSE2", GOARCH: "amd64",
			New: func() Arch { return newX86("sse2", modeSSE2, 4) }},
		{Name: "avx2", Suffix: "AVX2", GOARCH: "amd64",
			New: func() Arch { return newX86("avx2", modeAVX2, 8) }},
		{Name: "avx512", Suffix: "AVX512", GOARCH: "amd64",
			New: func() Arch { return newX86("avx512", modeAVX, 4) }},
		{Name: "neon", Suffix: "NEON", GOARCH: "arm64",
			New: func() Arch { return newNEON(4) }},
		{Name: "neonhybrid", Suffix: "NEONHybrid", GOARCH: "arm64",
			New: func() Arch { return newNEONHybrid("neonhybrid", 4, 4) }},
		{Name: "neonhybrid2", Suffix: "NEONHybrid2", GOARCH: "arm64",
			New: func() Arch { return newNEONHybrid("neonhybrid2", 8, 2) }},
		{Name: "sve2vl128", Suffix: "SVE2VL128", GOARCH: "arm64", VL: 16,
			New: func() Arch { return newSVE2(16, 2) }},
		{Name: "sve2vl256", Suffix: "SVE2VL256", GOARCH: "arm64", VL: 32,
			New: func() Arch { return newSVE2(32, 4) }},
		{Name: "sve2vl512", Suffix: "SVE2VL512", GOARCH: "arm64", VL: 64,
			New: func() Arch { return newSVE2(64, 4) }},
	}
}

// Filename is where this backend's assembly goes. The architecture suffix is
// what constrains the build, so it has to be last.
func (b Backend) Filename() string {
	return fmt.Sprintf("xxh_%s_%s.s", b.Name, b.GOARCH)
}
