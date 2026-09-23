//go:build !arm64

package xxh64

// The primes the Go code multiplies and adds by. On arm64 these are
// variables rather than constants, in konst_arm64.go: the compiler there
// builds a 64-bit constant with a movz and three movk, four instructions,
// where a load from memory is an adrp and an ldr, and the paths that use
// them -- the fixed-size entry points and the Digest's finalization -- are
// short enough to be bound by how many instructions they dispatch. Here
// they stay constants, which amd64 encodes in the instruction that uses them.
const (
	kPrime1 = prime1
	kPrime2 = prime2
	kPrime3 = prime3
	kPrime4 = prime4
	kPrime5 = prime5
)

// completeInGo says Digest.write completes a staged block with complete,
// and copies what is left over with copySmall, rather than with memmove and
// a kernel call. It is off here because it was measured on arm64 only.
const completeInGo = false
