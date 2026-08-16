//go:build arm64 && !linux && !purego

package xxh3

// Off Linux there is no portable way to ask for SVE2 without cgo, and no
// arm64 platform outside Linux currently exposes it, so the NEON kernel is
// used unconditionally.

func hasSVE2() bool { return false }

// hybridCore reports whether this core runs the hybrid kernel faster. Off
// Linux the core cannot be identified without cgo, so it never does.
const hybridCore = false
