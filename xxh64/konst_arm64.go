//go:build arm64

package xxh64

// The primes the Go code multiplies and adds by, as variables: see
// konst_other.go. Measured on a Neoverse N2: the fixed-size entry points
// 15-32% faster, Digest.Sum64 8-18%.
var (
	kPrime1 = prime1
	kPrime2 = prime2
	kPrime3 = prime3
	kPrime4 = prime4
	kPrime5 = prime5
)

// completeInGo says Digest.write completes a staged block with complete,
// and copies what is left over with copySmall; see there for what that
// measured on a Neoverse N2.
const completeInGo = true
