//go:build amd64 && !purego

package xxh64

import "unsafe"

// amd64 has one kernel: the lane loop is bound by the integer multiplier
// whatever the surrounding code does, so there is nothing to select. These
// wrappers inline into the public entry points.

func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Scalar(p, n, seed) }

func blocks(v *[4]uint64, p unsafe.Pointer, nb int) { blocksScalar(v, p, nb) }

// Backend names the kernel in use.
func Backend() string { return "scalar" }

// setBackend forces a backend, for tests. There is only the one here.
func setBackend(name string) bool { return name == "scalar" }
