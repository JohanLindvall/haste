//go:build purego || (!amd64 && !arm64)

package xxh3

import "unsafe"

// hashLong, accumStripes and scrambleAcc are the three entry points every
// backend provides. Everything above them is architecture-independent Go;
// everything below is either the portable loop in generic.go or a generated
// assembly kernel.
//
//   - hashLong runs the whole long-input loop: blocks, scrambles, the trailing
//     stripes and the overlapping final stripe. One call per one-shot hash.
//   - accumBlocks runs a stripe count from a given position within the current
//     block, scrambling at every boundary it crosses. This is the streaming
//     path, and it keeps the accumulators in registers across those boundaries.
//   - accumStripes runs a plain run of stripes against one secret position,
//     which is what the final stripe of a streamed input needs.

func hashLong(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) {
	hashLongGeneric(acc, in, n, sec, secretLimit)
}

func accumBlocks(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int) {
	accumBlocksGeneric(acc, in, nbStripes, sec, secretLimit, soFar)
}

// accumBlocksStream is what the streaming path calls; on amd64 it is a
// function variable that skips the dispatch switch, which measured 18% of a
// 256-byte Write there. Here it is a wrapper the compiler inlines, so absorb
// compiles to exactly the call it made before the variable existed.
func accumBlocksStream(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit, soFar int) {
	accumBlocks(acc, in, nbStripes, sec, secretLimit, soFar)
}

func accumStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer) {
	accumulateGeneric(acc, in, sec, nbStripes)
}

// Backend names the kernel selected for this machine.
func Backend() string { return "generic" }

// setBackend exists so that tests can be written once for every build; there
// is nothing to select here.
func setBackend(name string) bool { return name == "generic" }
