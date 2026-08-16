//go:build arm64 && linux && !purego

package xxhaste

import (
	"encoding/binary"
	"os"
	_ "unsafe" // for go:linkname

	"github.com/JohanLindvall/xxhaste/internal/cpu"
)

// SVE2 detection on Linux.
//
// The kernel reports it through the auxiliary vector, which the Go runtime has
// already parsed at startup; reading it from there costs nothing and avoids a
// cgo dependency on getauxval. If that ever stops being reachable, the same
// data is still readable from /proc/self/auxv.

const (
	atHWCap  = 16
	atHWCap2 = 26

	hwcapSVE   = 1 << 22 // HWCAP_SVE
	hwcap2SVE2 = 1 << 1  // HWCAP2_SVE2
)

//go:linkname runtimeGetAuxv runtime.getAuxv
func runtimeGetAuxv() []uintptr

// Core identification.
//
// One kernel here is faster only on cores whose vector pipes are scarce
// relative to their integer pipes, and slower on the rest, so it is enabled
// only where the core is positively identified. MIDR_EL1 names the exact
// core, which means this check can fail to recognise a core but cannot
// misidentify one: anything unrecognised keeps the portable choice. Reading
// it is internal/cpu's job, shared with xxh64.

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

var sve2 = detectSVE2()

func hasSVE2() bool { return sve2 }

func detectSVE2() bool {
	hwcap, hwcap2 := auxvCaps()
	return hwcap&hwcapSVE != 0 && hwcap2&hwcap2SVE2 != 0
}

func auxvCaps() (hwcap, hwcap2 uint64) {
	av := runtimeGetAuxv()
	for i := 0; i+1 < len(av); i += 2 {
		switch av[i] {
		case atHWCap:
			hwcap = uint64(av[i+1])
		case atHWCap2:
			hwcap2 = uint64(av[i+1])
		}
	}
	if hwcap == 0 {
		return auxvFromProc()
	}
	return hwcap, hwcap2
}

// auxvFromProc is the fallback for a runtime that no longer exposes auxv.
func auxvFromProc() (hwcap, hwcap2 uint64) {
	b, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return 0, 0
	}
	for i := 0; i+16 <= len(b); i += 16 {
		key := binary.LittleEndian.Uint64(b[i:])
		val := binary.LittleEndian.Uint64(b[i+8:])
		switch key {
		case atHWCap:
			hwcap = val
		case atHWCap2:
			hwcap2 = val
		}
	}
	return hwcap, hwcap2
}
