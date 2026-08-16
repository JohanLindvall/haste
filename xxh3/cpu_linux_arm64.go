//go:build arm64 && linux && !purego

package xxh3

import (
	"encoding/binary"
	"os"
	_ "unsafe" // for go:linkname
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
