//go:build !(386 || amd64 || arm64 || loong64 || ppc64le || riscv64 || wasm)

package xxh64

import (
	"encoding/binary"
	"unsafe"
)

// Everywhere else the load is spelled out: either the architecture is
// big-endian and needs the swap, or unaligned access traps and the word must
// be assembled from bytes. Going through encoding/binary leaves that choice
// to the compiler.

func rd64(p unsafe.Pointer, off int) uint64 {
	return binary.LittleEndian.Uint64(unsafe.Slice((*byte)(unsafe.Add(p, off)), 8))
}

func rd32(p unsafe.Pointer, off int) uint32 {
	return binary.LittleEndian.Uint32(unsafe.Slice((*byte)(unsafe.Add(p, off)), 4))
}
