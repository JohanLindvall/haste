//go:build purego || (!amd64 && !arm64)

package xxhaste

import "unsafe"

// hashLong, accumStripes and scrambleAcc are the three entry points every
// backend provides. Everything above them is architecture-independent Go;
// everything below is either the portable loop in generic.go or a generated
// assembly kernel.
//
//   - hashLong runs the whole long-input loop: blocks, scrambles, the trailing
//     stripes and the overlapping final stripe. One call per one-shot hash.
//   - accumStripes runs nbStripes stripes and nothing else, for the streaming
//     path, which has to stop at block boundaries itself.
//   - scrambleAcc is that boundary step.

func hashLong(acc *[accNB]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) {
	hashLongGeneric(acc, in, n, sec, secretLimit)
}

func accumStripes(acc *[accNB]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer) {
	accumulateGeneric(acc, in, sec, nbStripes)
}

func scrambleAcc(acc *[accNB]uint64, sec unsafe.Pointer) {
	scrambleGeneric(acc, sec)
}

// Backend names the kernel selected for this machine.
func Backend() string { return "generic" }

// setBackend exists so that tests can be written once for every build; there
// is nothing to select here.
func setBackend(name string) bool { return name == "generic" }
