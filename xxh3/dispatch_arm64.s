// Hand-written, not generated: the kernel entry points, each a tail
// jump to the kernel dispatch picked. See dispatch_arm64.go for why this is
// assembly rather than a Go switch.

//go:build arm64 && !purego

#include "textflag.h"

// The backend numbering is dispatch_arm64.go's: NEON is 0, the four-lane
// hybrid 1, the two-lane hybrid 2, and the SVE2 kernels 3, 4 and 5 by
// vector length. The kernels are ABI0 with the same argument frame, so a
// jump lands them on this function's arguments and their RET returns to
// this function's caller.
//
// The hybrid is tested first and falls through to its jump: it is what
// the Neoverse cores this repository is measured on take, and a dispatch
// that costs them a byte load, a compare, a not-taken branch and the jump
// is the least any dispatch can cost. Plain NEON, which every other core
// without SVE2 takes, is next.

// func hashLong(acc *[8]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)
TEXT ·hashLong(SB), NOSPLIT, $0-40
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·hashLongNEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·hashLongNEONHybrid2(SB)
neon:
	JMP	·hashLongNEON(SB)
vl256:
	JMP	·hashLongSVE2VL256(SB)
vl512:
	JMP	·hashLongSVE2VL512(SB)
vl128:
	JMP	·hashLongSVE2VL128(SB)

// func accumBlocks(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit int, soFar int)
TEXT ·accumBlocks(SB), NOSPLIT, $0-48
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·accumBlocksNEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·accumBlocksNEONHybrid2(SB)
neon:
	JMP	·accumBlocksNEON(SB)
vl256:
	JMP	·accumBlocksSVE2VL256(SB)
vl512:
	JMP	·accumBlocksSVE2VL512(SB)
vl128:
	JMP	·accumBlocksSVE2VL128(SB)

// func accumStripes(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer)
TEXT ·accumStripes(SB), NOSPLIT, $0-32
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·accumNEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·accumNEONHybrid2(SB)
neon:
	JMP	·accumNEON(SB)
vl256:
	JMP	·accumSVE2VL256(SB)
vl512:
	JMP	·accumSVE2VL512(SB)
vl128:
	JMP	·accumSVE2VL128(SB)

// func accumBlocks2(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit int, soFar int, in2 unsafe.Pointer, nbStripes2 int)
TEXT ·accumBlocks2(SB), NOSPLIT, $0-64
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·accumBlocks2NEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·accumBlocks2NEONHybrid2(SB)
neon:
	JMP	·accumBlocks2NEON(SB)
vl256:
	JMP	·accumBlocks2SVE2VL256(SB)
vl512:
	JMP	·accumBlocks2SVE2VL512(SB)
vl128:
	JMP	·accumBlocks2SVE2VL128(SB)

// func hashLong64(in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int) uint64
TEXT ·hashLong64(SB), NOSPLIT, $0-40
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·hashLong64NEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·hashLong64NEONHybrid2(SB)
neon:
	JMP	·hashLong64NEON(SB)
vl256:
	JMP	·hashLong64SVE2VL256(SB)
vl512:
	JMP	·hashLong64SVE2VL512(SB)
vl128:
	JMP	·hashLong64SVE2VL128(SB)

// func hashLong128(out *[2]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)
TEXT ·hashLong128(SB), NOSPLIT, $0-40
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·hashLong128NEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·hashLong128NEONHybrid2(SB)
neon:
	JMP	·hashLong128NEON(SB)
vl256:
	JMP	·hashLong128SVE2VL256(SB)
vl512:
	JMP	·hashLong128SVE2VL512(SB)
vl128:
	JMP	·hashLong128SVE2VL128(SB)

// func hashLongSeed64(in unsafe.Pointer, n int, seed uint64) uint64
//
// The seeded kernels have a frame, which each sets up for itself: a jump
// leaves it this function's caller's arguments and link register.
TEXT ·hashLongSeed64(SB), NOSPLIT, $0-32
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·hashLongSeed64NEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·hashLongSeed64NEONHybrid2(SB)
neon:
	JMP	·hashLongSeed64NEON(SB)
vl256:
	JMP	·hashLongSeed64SVE2VL256(SB)
vl512:
	JMP	·hashLongSeed64SVE2VL512(SB)
vl128:
	JMP	·hashLongSeed64SVE2VL128(SB)

// func hashLongSeed128(out *[2]uint64, in unsafe.Pointer, n int, seed uint64)
TEXT ·hashLongSeed128(SB), NOSPLIT, $0-32
	MOVBU	·backend(SB), R0
	CMP	$1, R0
	BNE	nothybrid
	JMP	·hashLongSeed128NEONHybrid(SB)
nothybrid:
	CBZ	R0, neon
	CMP	$4, R0
	BEQ	vl256
	CMP	$5, R0
	BEQ	vl512
	CMP	$3, R0
	BEQ	vl128
	JMP	·hashLongSeed128NEONHybrid2(SB)
neon:
	JMP	·hashLongSeed128NEON(SB)
vl256:
	JMP	·hashLongSeed128SVE2VL256(SB)
vl512:
	JMP	·hashLongSeed128SVE2VL512(SB)
vl128:
	JMP	·hashLongSeed128SVE2VL128(SB)

// func hashLongStaged(acc *[8]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)
//
// On arm64 the Digest's staged input takes the same kernels as any other;
// the width question dispatch_amd64.s answers for AVX-512 does not arise.
TEXT ·hashLongStaged(SB), NOSPLIT, $0-40
	JMP	·hashLong(SB)
