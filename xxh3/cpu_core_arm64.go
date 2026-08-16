//go:build arm64 && !purego

package xxh3

import "github.com/JohanLindvall/haste/internal/cpu"

// Core identification, on every arm64 platform that can name its core.
//
// One kernel here is faster only on cores whose vector pipes are scarce
// relative to their integer pipes, and slower on the rest, so it is enabled
// only where the core is positively identified. MIDR_EL1 names the exact
// core, which means this check can fail to recognise a core but cannot
// misidentify one: anything unrecognised keeps the portable choice. Reading
// it is internal/cpu's job -- sysfs on Linux, the registry on Windows --
// shared with xxh64.
//
// It lives here rather than beside the SVE2 detection because the two ask
// different questions of different platforms: SVE2 comes from an auxiliary
// vector that only Linux has, while a core's identity is available wherever
// the operating system will say. Keeping them together cost a Neoverse N2
// running Windows a quarter of its throughput on every hash above a
// kibibyte, because the file that knew which cores want the hybrid kernel
// was compiled only on Linux.

// narrowVectorCores lists cores measured or modelled to run the hybrid kernel
// faster, keyed by implementer<<12|part. It is deliberately short: every entry
// needs evidence, and the cost of omitting a core is only that it keeps the
// kernel it has.
//
//	0x41d49  ARM Neoverse N2  -- measured here, +24% at 64 KiB
//
// Cores known to lose: Neoverse V1 and V2 (four vector pipes, so the trade is
// backwards), Apple's (four vector pipes and a multiply-accumulate that
// issues once a cycle; measured 4-10% behind on an M2), and anything
// big.LITTLE, where a goroutine can migrate to a core the check never saw.
var narrowVectorCores = map[uint32]bool{
	0x41d49: true,
}

var hybridCore = detectHybridCore()

func detectHybridCore() bool {
	v, ok := cpu.MIDR()
	return ok && narrowVectorCores[cpu.Implementer(v)<<12|cpu.Part(v)]
}
