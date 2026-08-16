package bench

// Hooks into the vendored C reference (xxhash.h, v0.8.3 -- the exact revision
// the test vectors were generated from). cref.go assigns them when cgo is
// available; without a C compiler they stay nil, every benchmark row guards
// on that, and the suite degrades to the pure-Go comparison instead of
// failing to build.
//
// The one-shot hooks cross the cgo boundary once per hash, so their small
// sizes measure the cgo call as much as the hash -- meaningful from a few
// hundred bytes up, an overhead floor below. The streaming hooks run the
// whole chunk loop on the C side for exactly that reason.
var (
	cXXH3        func(b []byte) uint64
	cXXH3Seed    func(b []byte, seed uint64) uint64
	cXXH3128     func(b []byte) (lo, hi uint64)
	cXXH64       func(b []byte, seed uint64) uint64
	cXXH3Stream  func(b []byte, chunk int) uint64
	cXXH64Stream func(b []byte, chunk int) uint64
)
