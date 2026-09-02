// Hand-written, not generated: the four kernel entry points, each a tail
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
