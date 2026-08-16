package asmgen

import (
	"fmt"
	"path"
)

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
	VL int
	// Dir is the package directory the generated files belong to, relative
	// to the module root; empty is the root package.
	Dir string
	// Exactly one of these is set: New builds an XXH3 vector backend, New64
	// an XXH64 scalar one.
	New   func() Arch
	New64 func() XXH64Arch
}

// Defs returns the functions this backend generates, in emission order.
func (b Backend) Defs() []FuncDef {
	if b.New64 != nil {
		if k := b.New64(); k.UnseededTwin() {
			return XXH64FuncsNS(b.Suffix, k.Dual())
		}
		return XXH64Funcs(b.Suffix, b.New64().Dual())
	}
	return Funcs(b.Suffix)
}

// EmitAll emits every function of this backend.
func (b Backend) EmitAll() []Kernel {
	if b.New64 != nil {
		return EmitXXH64(b.New64)
	}
	var ks []Kernel
	for _, a := range EmitAll(b.New) {
		ks = append(ks, a)
	}
	return ks
}

// Package is the Go package the generated files belong to.
func (b Backend) Package() string {
	if b.Dir == "" {
		return "xxhaste"
	}
	return path.Base(b.Dir)
}

// AllBackends is everything the generator produces: the XXH3 backends and the
// XXH64 ones.
func AllBackends() []Backend {
	return append(Backends(), XXH64Backends()...)
}

// XXH64Backends lists the scalar XXH64 kernels, which live in the xxh64
// package: one per architecture. The arm64 one carries both forms of the
// lane round; see arm64_xxh64.go for which core wants which.
func XXH64Backends() []Backend {
	return []Backend{
		{Name: "scalar", Suffix: "Scalar", GOARCH: "amd64", Dir: "xxh64",
			New64: func() XXH64Arch { return newX86Scalar() }},
		{Name: "scalar", Suffix: "Scalar", GOARCH: "arm64", Dir: "xxh64",
			New64: func() XXH64Arch { return newARM64Scalar() }},
	}
}

// Backends lists the XXH3 backends.
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

// Filename is where this backend's assembly goes, relative to the module
// root. The architecture suffix is what constrains the build, so it has to be
// last.
func (b Backend) Filename() string {
	return path.Join(b.Dir, fmt.Sprintf("xxh_%s_%s.s", b.Name, b.GOARCH))
}
