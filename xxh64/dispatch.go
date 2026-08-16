//go:build purego || (!amd64 && !arm64)

package xxh64

import "unsafe"

// Without a kernel, both entry points are the portable implementation.

func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Generic(p, n, seed) }

// sum64NS is the unseeded entry points' route to the kernel. Only amd64
// generates a twin for it; here it is the seeded path with a zero, which is
// what the portable build did before the twin existed.
func sum64NS(p unsafe.Pointer, n int) uint64 { return sum64Generic(p, n, 0) }

func blocks(v *[4]uint64, p unsafe.Pointer, nb int) { blocksGeneric(v, p, nb) }

// Backend names the kernel in use.
func Backend() string { return "generic" }

// setBackend exists so that tests can be written once for every build; there
// is nothing to select here.
func setBackend(name string) bool { return name == "generic" }
