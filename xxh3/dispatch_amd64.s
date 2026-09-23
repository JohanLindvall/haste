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

// accumBlocks, accumStripes and hashLongStaged read the Digest's staging
// buffer, which the writes before them have just filled with 16-byte moves,
// and on a machine with AVX-512 they take the AVX2 kernels rather than their
// own. A load cannot be forwarded from several narrower stores, so a stripe
// read before those stores reach the cache waits for them, and a 64-byte
// load spans four of them where a 32-byte one spans two. Measured on a
// Zen 4, with each kernel otherwise equal: a message written whole and then
// summed, 21.5 store-to-load-interlock failures per call at 256 bytes on
// AVX-512 against 14.8 on AVX2, and 8-21% of the time over 241..1024
// bytes; a mebibyte streamed in 64..960-byte writes, whose drains run
// accumBlocks over the staging area, 2-8%. accumBlocks2's second run comes
// straight from the caller's slice, which nothing has just written, and a
// large write's worth of it keeps the AVX-512 kernel's 5-13% there.

// func accumBlocks(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer, secretLimit int, soFar int)
TEXT ·accumBlocks(SB), NOSPLIT, $0-48
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $1
	JAE	avx2
	JMP	·accumBlocksSSE2(SB)
avx2:
	JMP	·accumBlocksAVX2(SB)

// func accumStripes(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer)
TEXT ·accumStripes(SB), NOSPLIT, $0-32
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $1
	JAE	avx2
	JMP	·accumSSE2(SB)
avx2:
	JMP	·accumAVX2(SB)

// func hashLongStaged(acc *[8]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)
TEXT ·hashLongStaged(SB), NOSPLIT, $0-40
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $1
	JAE	avx2
	JMP	·hashLongSSE2(SB)
avx2:
	JMP	·hashLongAVX2(SB)

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

// func hashLongSeed(keys *[16]uint64, in unsafe.Pointer, n int, seed uint64)
TEXT ·hashLongSeed(SB), NOSPLIT, $0-32
	MOVBLZX	·backend(SB), AX
	CMPB	AL, $2
	JEQ	avx512
	CMPB	AL, $1
	JEQ	avx2
	JMP	·hashLongSeedSSE2(SB)
avx2:
	JMP	·hashLongSeedAVX2(SB)
avx512:
	JMP	·hashLongSeedAVX512(SB)
