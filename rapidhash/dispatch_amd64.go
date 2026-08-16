//go:build amd64 && !purego

package rapidhash

import "unsafe"

// amd64 has one kernel. rapidhash's only primitive is a 64x64 multiply, and
// on x86 that is mulq, which is baseline and has no alternative worth
// choosing between at runtime -- BMI2's mulx would be a second kernel, and
// see x86_rapid.go in the generator for why there is not one yet.

// sum64 hashes n bytes at p under seed in one call into the kernel, whatever
// n is. It inlines into the public entry points, so a hash costs one call.
func sum64(p unsafe.Pointer, n int, seed uint64) uint64 { return sum64Rapid(p, n, seed) }
// sum64NS is what Sum64 and Sum64String reach: the same hash with the seed
// known to be zero, so the prologue's mix is a constant rather than a
// multiply. The entry point settles which kernel a caller gets at compile
// time, so it costs no branch.
func sum64NS(p unsafe.Pointer, n int) uint64 { return sum64RapidNS(p, n) }


// Backend names the kernel in use.
func Backend() string { return "scalar" }

// setBackend forces a backend, for tests and benchmarks.
func setBackend(name string) bool { return name == "scalar" }

// backendNames lists every kernel this build could dispatch to.
func backendNames() []string { return []string{"scalar"} }
