//go:build !(arm64 && (linux || windows))

package cpu

// MIDR is readable through sysfs on Linux and the registry on Windows;
// everywhere else it is unknown, and callers keep their default. macOS is
// the notable everywhere-else: it publishes no such thing, and Apple is
// identified by the platform instead -- see Apple in cpu.go.
func MIDR() (uint64, bool) { return 0, false }
