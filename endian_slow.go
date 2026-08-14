//go:build !(386 || amd64 || arm64 || loong64 || ppc64le || riscv64 || wasm)

package xxhaste

import (
	"encoding/binary"
	"unsafe"
)

// Everywhere else the load is spelled out: either the architecture is
// big-endian (s390x, ppc64, mips, mips64) and needs the swap, or unaligned
// access traps and must be assembled from bytes (mips, mipsle, arm). Going
// through encoding/binary leaves that choice to the compiler, which knows
// which of the two applies.

func rd64(p unsafe.Pointer, off uintptr) uint64 {
	return binary.LittleEndian.Uint64(unsafe.Slice((*byte)(unsafe.Add(p, off)), 8))
}

func rd32(p unsafe.Pointer, off uintptr) uint32 {
	return binary.LittleEndian.Uint32(unsafe.Slice((*byte)(unsafe.Add(p, off)), 4))
}
