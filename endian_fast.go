//go:build 386 || amd64 || arm64 || loong64 || ppc64le || riscv64 || wasm

package xxhaste

import "unsafe"

// On these architectures an unaligned little-endian load is a single
// instruction, so the reads that dominate the short-input paths cost exactly
// one memory access with no bounds check and no byte shuffling.

func rd64(p unsafe.Pointer, off uintptr) uint64 { return *(*uint64)(unsafe.Add(p, off)) }
func rd32(p unsafe.Pointer, off uintptr) uint32 { return *(*uint32)(unsafe.Add(p, off)) }
