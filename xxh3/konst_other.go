//go:build !arm64

package xxh3

// The 64-bit multipliers and keys the Go paths use. They are constants
// here, which amd64 encodes in the instructions that use them; on arm64
// they are variables, in konst_arm64.go, because a load is half the
// instructions of building one there.
const (
	kAvalanche = 0x165667919E3779F9
	kRRMXMX    = 0x9FB21C651E98DF25
	kPrime64_1 = prime64_1
	kPrime64_2 = prime64_2
	kPrime64_3 = prime64_3
	kPrime64_4 = prime64_4

	kBitflip4to8 = bitflip4to8
	kBitflip9lo  = bitflip9lo
	kBitflip9hi  = bitflip9hi
)

// drainMax is the longest write absorb stages rather than hashing straight
// out of the caller's slice: below it, the staged whole stripes are drained
// in one kernel call and p is copied in; from it up, the write goes through
// the kernel from where it lies. The bound also keeps the drain's slide in
// bounds: at most 63 staged bytes remain, so window + remainder + p fits.
const drainMax = internalBufferSize - (stripeLen - 1)
