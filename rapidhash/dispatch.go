//go:build purego || (!amd64 && !arm64)

package rapidhash

import "unsafe"

// The portable path: every architecture without a generated kernel, and any
// build with the purego tag. It is also the oracle the kernels are checked
// against, in asmsim_test.go and in TestKernelsMatchPortable.

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
