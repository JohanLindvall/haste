//go:build 386 || amd64 || arm64 || loong64 || ppc64le || riscv64 || wasm

package rapidhash

import "unsafe"

// On these architectures an unaligned little-endian load is a single
// instruction, so the reads that dominate the short-input path cost exactly
// one memory access with no bounds check and no byte shuffling. rapidhash reads both ways in the short path. Same split as
// the parent package.

func rd64(p unsafe.Pointer, off int) uint64 { return *(*uint64)(unsafe.Add(p, off)) }
func rd32(p unsafe.Pointer, off int) uint32 { return *(*uint32)(unsafe.Add(p, off)) }
