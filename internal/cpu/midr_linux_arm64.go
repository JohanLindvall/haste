//go:build arm64 && linux

package cpu

import (
	"os"
	"strconv"
	"strings"
)

// MIDR_EL1 names the exact core, which means a check against it can fail to
// recognise a core but cannot misidentify one: anything unrecognised keeps
// the portable choice. The register is not readable from user mode on arm64;
// Linux publishes it per CPU in sysfs, and there is no cheaper way to ask.
const midrPath = "/sys/devices/system/cpu/cpu0/regs/identification/midr_el1"

var midr, midrOK = readMIDR()

func readMIDR() (uint64, bool) {
	b, err := os.ReadFile(midrPath)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// MIDR returns the first CPU's MIDR_EL1, if it could be read.
func MIDR() (uint64, bool) { return midr, midrOK }
