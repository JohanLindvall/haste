// Hand-written, not generated: one instruction is all the prime-form choice
// needs. The parent package has its own copy for the same reason -- xxhaste
// imports nothing, and that includes itself.

//go:build amd64 && !purego

#include "textflag.h"

// func cpuid(eaxArg uint32) (eax, ebx, ecx, edx uint32)
TEXT ·cpuid(SB), NOSPLIT, $0-24
	MOVL eaxArg+0(FP), AX
	XORL CX, CX
	CPUID
	MOVL AX, eax+8(FP)
	MOVL BX, ebx+12(FP)
	MOVL CX, ecx+16(FP)
	MOVL DX, edx+20(FP)
	RET
