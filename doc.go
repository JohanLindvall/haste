// Package haste is the module root; the hashes live in its subpackages.
// Nothing is exported here.
//
//   - [github.com/JohanLindvall/haste/xxh3] — XXH3, 64- and 128-bit, with
//     generated SSE2, AVX2, AVX-512, NEON and SVE2 kernels, one-shot and
//     streaming. The one to reach for unless you need to match something.
//   - [github.com/JohanLindvall/haste/xxh64] — XXH64, the older scalar
//     member of the family, with generated kernels for amd64 and arm64.
//     What most Go code that says "xxhash" computes today.
//   - [github.com/JohanLindvall/haste/rapidhash] — rapidhash, wyhash's
//     successor: folded 64x64 multiplies and no vector unit at all.
//
// All three are bit-identical to their reference C implementations and are
// checked against vectors taken from them.
//
// XXH3 was at this import path until it moved to xxh3/, which is a breaking
// change for code that imported it here: add /xxh3 to the import and the
// identifiers are unchanged.
package haste

// The generator writes into the packages above; -out is the module root, so
// the directive belongs here rather than beside the assembly it produces.
//
//go:generate go run ./internal/asmgen/gen -out .
