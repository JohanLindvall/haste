//go:build purego || (!amd64 && !arm64)

package xxh3

import "unsafe"

// hashLong, accumBlocks, accumBlocks2 and accumStripes are the four entry
// points every backend provides. Everything above them is
// architecture-independent Go; everything below is either the portable loop
// in generic.go or a generated assembly kernel.
//
//   - hashLong runs the whole long-input loop: blocks, scrambles, the trailing
//     stripes and the overlapping final stripe. One call per one-shot hash.
//   - accumBlocks runs a stripe count from a given position within the current
//     block, scrambling at every boundary it crosses. This is the streaming
//     path, and it keeps the accumulators in registers across those boundaries.
//   - accumBlocks2 is accumBlocks over two runs -- the stripes staged in the
//     Digest and then the ones straight out of the caller's slice -- as one
//     walk of the block, so that a large write costs one call rather than
//     two.
//   - accumStripes runs a plain run of stripes against one secret position,
//     which is what the final stripe of a streamed input needs.

func hashLong(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) {
	hashLongGeneric(acc, in, n, sec, secretLimit)
}

func accumBlocks(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int) {
	accumBlocksGeneric(acc, in, nbStripes, sec, secretLimit, soFar)
}

func accumBlocks2(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int, in2 unsafe.Pointer, nbStripes2 int) {
	accumBlocksGeneric(acc, in, nbStripes, sec, secretLimit, soFar)
	accumBlocksGeneric(acc, in2, nbStripes2, sec, secretLimit, (soFar+nbStripes)%(secretLimit/secretConsumeRate))
}

func accumStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer) {
	accumulateGeneric(acc, in, sec, nbStripes)
}

// Backend names the kernel selected for this machine.
func Backend() string { return "generic" }

// setBackend exists so that tests can be written once for every build; there
// is nothing to select here.
func setBackend(name string) bool { return name == "generic" }
