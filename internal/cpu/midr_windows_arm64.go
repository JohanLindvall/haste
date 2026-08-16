//go:build windows && arm64

package cpu

import (
	"syscall"
	"unsafe"
)

// MIDR_EL1 on Windows.
//
// There is no sysfs to read it out of and the register itself is EL1-only:
// Linux traps the MRS and emulates it, Windows does not, so executing one
// would fault. What Windows does instead is publish the processor's system
// registers under its registry key, MIDR_EL1 as the value "CP 4000" -- the
// name is the register's encoding, and the same key carries the others.
//
// This matters more than an identification usually would. The runner that
// found it is a Neoverse N2 whose kernel timings match a Linux N2 to within
// 0.3%, and which was taking the plain NEON kernel because nothing here
// could name it: 3,149 ns against 2,297 for the hybrid at 64 KiB, a quarter
// of the throughput of every large hash on the platform.
//
// Confirmed on that runner rather than argued from here. It now reports
// neon-hybrid, and XXH3 through the public API moved +15.6% at 1 KiB, +31.0%
// at 4 KiB, +36.3% at 16 KiB, +37.0% at 64 KiB and +25.7% at a mebibyte,
// with the three kernels themselves timing identically either side.
const (
	midrKey   = `HARDWARE\DESCRIPTION\System\CentralProcessor\0`
	midrValue = "CP 4000"
)

var midr, midrOK = readMIDR()

func readMIDR() (uint64, bool) {
	key, err := syscall.UTF16PtrFromString(midrKey)
	if err != nil {
		return 0, false
	}
	var h syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, key, 0, syscall.KEY_READ, &h); err != nil {
		return 0, false
	}
	defer syscall.RegCloseKey(h)

	name, err := syscall.UTF16PtrFromString(midrValue)
	if err != nil {
		return 0, false
	}
	// MIDR_EL1 is 32 bits wide, but which registry type it arrives as is not
	// promised, so anything four or eight bytes long is accepted and read
	// little-endian.
	var buf [8]byte
	n := uint32(len(buf))
	var typ uint32
	if err := syscall.RegQueryValueEx(h, name, nil, &typ, &buf[0], &n); err != nil {
		return 0, false
	}
	switch n {
	case 4:
		return uint64(*(*uint32)(unsafe.Pointer(&buf[0]))), true
	case 8:
		return *(*uint64)(unsafe.Pointer(&buf[0])), true
	}
	return 0, false
}

// MIDR returns this processor's MIDR_EL1, if the registry had it.
func MIDR() (uint64, bool) { return midr, midrOK }
