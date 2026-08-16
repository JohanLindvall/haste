package bench

// The reference implementation itself, compiled from the vendored xxhash.h
// (v0.8.3, BSD-2-Clause, header retained in the file). XXH_INLINE_ALL gives
// the compiler the whole implementation to specialize, which is how the
// reference is meant to be consumed for speed; -march=native on amd64 lets
// it pick the same vector width the runtime-dispatched Go kernels do.
//
// The streaming helpers live in C so a streamed rep costs one cgo transition
// instead of one per Write; otherwise the boundary, not the reference, is
// what gets measured. The states are allocated once at init and never freed:
// they live exactly as long as the benchmark process.

/*
#cgo CFLAGS: -O3
#cgo amd64 CFLAGS: -march=native
#define XXH_INLINE_ALL
#include "xxhash.h"

static XXH3_state_t  *benchState3;
static XXH64_state_t *benchState64;

static int benchInit(void) {
	benchState3  = XXH3_createState();
	benchState64 = XXH64_createState();
	return benchState3 != NULL && benchState64 != NULL;
}

static uint64_t benchStream3(const char *p, size_t n, size_t chunk) {
	XXH3_64bits_reset(benchState3);
	for (size_t off = 0; off < n; off += chunk) {
		size_t c = chunk;
		if (n - off < c) c = n - off;
		XXH3_64bits_update(benchState3, p + off, c);
	}
	return XXH3_64bits_digest(benchState3);
}

static uint64_t benchStream64(const char *p, size_t n, size_t chunk) {
	XXH64_reset(benchState64, 0);
	for (size_t off = 0; off < n; off += chunk) {
		size_t c = chunk;
		if (n - off < c) c = n - off;
		XXH64_update(benchState64, p + off, c);
	}
	return XXH64_digest(benchState64);
}
*/
import "C"

import "unsafe"

// cptr is the C view of b. The reference accepts NULL for an empty input,
// and an empty Go slice may have no backing array to point at.
func cptr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

func init() {
	if C.benchInit() == 0 {
		return // out of memory at init; leave the hooks nil
	}
	cXXH3 = func(b []byte) uint64 {
		return uint64(C.XXH3_64bits(cptr(b), C.size_t(len(b))))
	}
	cXXH3Seed = func(b []byte, seed uint64) uint64 {
		return uint64(C.XXH3_64bits_withSeed(cptr(b), C.size_t(len(b)), C.XXH64_hash_t(seed)))
	}
	cXXH3128 = func(b []byte) (lo, hi uint64) {
		h := C.XXH3_128bits(cptr(b), C.size_t(len(b)))
		return uint64(h.low64), uint64(h.high64)
	}
	cXXH64 = func(b []byte, seed uint64) uint64 {
		return uint64(C.XXH64(cptr(b), C.size_t(len(b)), C.XXH64_hash_t(seed)))
	}
	cXXH3Stream = func(b []byte, chunk int) uint64 {
		return uint64(C.benchStream3((*C.char)(cptr(b)), C.size_t(len(b)), C.size_t(chunk)))
	}
	cXXH64Stream = func(b []byte, chunk int) uint64 {
		return uint64(C.benchStream64((*C.char)(cptr(b)), C.size_t(len(b)), C.size_t(chunk)))
	}
}
