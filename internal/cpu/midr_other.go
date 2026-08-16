//go:build !(arm64 && linux)

package cpu

// MIDR is only readable through sysfs on Linux; everywhere else it is
// unknown, and callers keep their default.
func MIDR() (uint64, bool) { return 0, false }
