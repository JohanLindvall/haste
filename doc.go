// Package xxhaste implements XXH3, the 64-bit and 128-bit hash from xxHash
// v0.8.3, with assembly kernels for SSE2, AVX2, AVX-512, NEON and SVE2.
//
// The output is bit-identical to the reference implementation on every path,
// including seeds and custom secrets. Every backend is checked against vectors
// taken from the C implementation.
//
// # Choosing an entry point
//
// For a whole slice or string, use [Sum64], [Sum64String], [Sum128] or
// [Sum128String]. These do not allocate and are small enough to be inlined
// into the caller, so hashing a short key costs a single call.
//
// For input arriving in pieces, use a [Digest] from [New]. It implements
// [hash.Hash64], and additionally offers [Digest.Sum128]: both widths come out
// of the same pass, so nothing has to be hashed twice.
//
// A seed keys the hash without changing its cost for inputs up to 240 bytes.
// Above that, XXH3 defines the seeded hash in terms of a secret derived from
// the seed, which [Sum64Seed] has to build on every call; code hashing many
// long inputs under one seed should use [NewSeed] instead, which derives it
// once. [Sum64Secret] takes a caller-supplied secret directly.
//
// # Backends
//
// The kernel is chosen once, at package initialization, from what the CPU
// reports: AVX-512, AVX2 or SSE2 on amd64, and SVE2 or NEON on arm64. SSE2 and
// NEON are part of their architecture's baseline, so there is always a vector
// kernel; [Backend] names the one in use. Building with the "purego" tag
// selects the portable implementation everywhere, and any other architecture
// uses it automatically.
//
// The assembly is generated: see internal/asmgen, and CLAUDE.md for how it is
// built and verified.
//
// The older 64-bit hash of the family, XXH64, is the subpackage
// [github.com/JohanLindvall/xxhaste/xxh64], built the same way.
package xxhaste

//go:generate go run ./internal/asmgen/gen -out .
