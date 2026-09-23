//go:build arm64

package xxh3

// The 64-bit multipliers and keys the Go paths use, as variables: the
// compiler builds a 64-bit constant on arm64 with a movz and three movk, and
// loads a variable with an adrp and an ldr, half the instructions. The paths
// that use them -- the 0..240-byte hashes, the fixed-size entry points, the
// Digest's finalization -- are short enough that a Neoverse N2 runs them at
// its dispatch width, where instructions are cycles. Measured on one, every
// length 0..256 through bench/sweep: the 64-bit hash 9% faster at 0..3
// bytes, 4% at 4..16, 7% at 17..32 and 2-5% on to 240; the fixed-size
// entry points 25-30%. See konst_other.go for everywhere else.
var (
	kAvalanche = uint64(0x165667919E3779F9)
	kRRMXMX    = uint64(0x9FB21C651E98DF25)
	kPrime64_1 = uint64(prime64_1)
	kPrime64_2 = uint64(prime64_2)
	kPrime64_3 = uint64(prime64_3)
	kPrime64_4 = uint64(prime64_4)

	kBitflip4to8 = uint64(bitflip4to8)
	kBitflip9lo  = uint64(bitflip9lo)
	kBitflip9hi  = uint64(bitflip9hi)
)

// drainMax is the longest write absorb stages rather than hashing straight
// out of the caller's slice; see konst_other.go. On a Neoverse N2 a kernel
// call over the write is cheaper than copying it from 256 bytes up, where
// the Zen 4 the other value was tuned on drew the line at 961: measured per
// mebibyte streamed, against 961, 512-byte writes 9% faster, 960-byte 16%,
// 256-byte 1-2%, and every other size level; with the line at 128 instead,
// 128-byte writes were 4% slower.
const drainMax = 256
