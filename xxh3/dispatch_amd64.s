// Hand-written, not generated: the four kernel entry points, each a tail
// jump to the kernel dispatch picked. See dispatch_amd64.go for why this is
// assembly rather than a Go switch.

//go:build amd64 && !purego

#include "textflag.h"

// The backend numbering is dispatch_amd64.go's: SSE2 is 0, AVX2 is 1 and
// AVX-512 is 2. Each dispatcher tests for AVX-512 first, then AVX2, and
// falls through to SSE2: the kernels are ABI0 with the same argument frame,
// so a jump lands them on this function's arguments and their RET returns
// to this function's caller.

// func hashLong(acc *[8]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)
TEXT ·hashLong(SB), NOSPLIT, $0-40
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $2
	JEQ	avx512
	CMPB	AL, $1
	JEQ	avx2
	JMP	·hashLongSSE2(SB)
avx2:
	JMP	·hashLongAVX2(SB)
avx512:
	JMP	·hashLongAVX512(SB)

// func accumBlocks(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit int, soFar int)
TEXT ·accumBlocks(SB), NOSPLIT, $0-48
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $2
	JEQ	avx512
	CMPB	AL, $1
	JEQ	avx2
	JMP	·accumBlocksSSE2(SB)
avx2:
	JMP	·accumBlocksAVX2(SB)
avx512:
	JMP	·accumBlocksAVX512(SB)

// func accumStripes(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer)
TEXT ·accumStripes(SB), NOSPLIT, $0-32
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $2
	JEQ	avx512
	CMPB	AL, $1
	JEQ	avx2
	JMP	·accumSSE2(SB)
avx2:
	JMP	·accumAVX2(SB)
avx512:
	JMP	·accumAVX512(SB)

// func accumBlocks2(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit int, soFar int, in2 unsafe.Pointer, nbStripes2 int)
TEXT ·accumBlocks2(SB), NOSPLIT, $0-64
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $2
	JEQ	avx512
	CMPB	AL, $1
	JEQ	avx2
	JMP	·accumBlocks2SSE2(SB)
avx2:
	JMP	·accumBlocks2AVX2(SB)
avx512:
	JMP	·accumBlocks2AVX512(SB)
