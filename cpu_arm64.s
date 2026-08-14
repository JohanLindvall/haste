// Hand-written, not generated: this is the one instruction the dispatcher
// needs before it knows whether SVE2 is safe to use.

//go:build arm64 && !purego

#include "textflag.h"

// func sveVectorLength() int
//
// RDVL reads the implemented vector length in bytes. Go's assembler has no SVE
// instructions at all, so it is emitted as its encoding; the caller must have
// established that the CPU has SVE before reaching this.
TEXT ·sveVectorLength(SB), NOSPLIT, $0-8
	WORD $0x04bf5020 // rdvl x0, #1
	MOVD R0, ret+0(FP)
	RET
