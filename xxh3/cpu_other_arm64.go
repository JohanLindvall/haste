//go:build arm64 && !linux && !purego

package xxh3

// Off Linux there is no portable way to ask for SVE2 without cgo, and no
// arm64 platform outside Linux currently exposes it, so SVE2 is never
// selected there. Which of the NEON kernels runs is decided by the core,
// which is a separate question with a separate answer per platform: see
// cpu_core_arm64.go.

func hasSVE2() bool { return false }
