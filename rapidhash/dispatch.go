package rapidhash

import "unsafe"

// There is one backend so far. rapidhash's only mixing primitive is the pair
// of halves of a 64x64 multiply, which internal/asmgen does not yet emit --
// the XXH64 scalar surface it has is built around primes, rotates and four
// lanes, and none of that carries over. Adding it is what an assembly backend
// here waits on; until then every architecture runs the portable code, which
// is the same code the kernels will be checked against when they arrive.

// sum64 is the single entry point a backend provides.
func sum64(p unsafe.Pointer, n int, seed uint64) uint64 {
	return sum64Generic(p, n, seed)
}

// Backend names the kernel in use.
func Backend() string { return "generic" }

// setBackend forces a backend, for tests and benchmarks. It reports whether
// the name is one this build can run.
func setBackend(name string) bool { return name == "generic" }

// backendNames lists every kernel a build of this package could dispatch to.
func backendNames() []string { return []string{"generic"} }
