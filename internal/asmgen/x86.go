package asmgen

import "fmt"

// The three x86 backends are one code generator parameterized by vector width.
// SSE2 has two-operand instructions and 128-bit registers, AVX2 gains a third
// operand and 256 bits, AVX-512 another 256 bits and sixteen more registers;
// the algorithm itself does not change, only how many stripes' worth of lanes
// fit in a register.

const (
	modeSSE2 = 128
	modeAVX2 = 256
	modeAVX  = 512
)

// x86 GPR numbering follows the hardware encoding, so R[n] in the Machine and
// the register names line up.
const (
	rAX GPR = 0
	rCX GPR = 1
	rDX GPR = 2
	rBX GPR = 3
	rSI GPR = 6
	rDI GPR = 7
	r8  GPR = 8
	r9  GPR = 9
	r10 GPR = 10
	r11 GPR = 11
	r12 GPR = 12
	r13 GPR = 13
	r14 GPR = 14
)

var x86GPRNames = map[GPR]string{
	rAX: "%rax", rCX: "%rcx", rDX: "%rdx", rBX: "%rbx",
	rSI: "%rsi", rDI: "%rdi", r8: "%r8", r9: "%r9",
	r10: "%r10", r11: "%r11", r12: "%r12", r13: "%r13",
	r14: "%r14",
}

// R14 holds the goroutine pointer in ABIInternal, but these kernels are ABI0,
// where it is undefined: the compiler re-establishes R14 (and X15) after every
// ABI0 call it makes, so an ABI0 leaf may clobber both. See
// cmd/compile/abi-internal.md, "transitions from ABIInternal to ABI0 can
// ignore these registers", and the code that does it in
// cmd/compile/internal/amd64/ssa.go. Only the XXH64 kernel takes R14 up on
// that; the vector pools below leave it, R15 and X15 alone.
//
// R15 is reserved under dynamic linking and X15 is the zero register, so
// neither appears above or in the vector pools below.
var x86ArgGPR = []GPR{rDI, rSI, rDX, rCX, r8, r9}

// rAX is last: Setup uses it to stage the broadcast constant, and does so
// before any kernel has a temporary live.
var x86TmpGPR = []GPR{r10, r11, r12, r13, rBX, rAX}

type x86 struct {
	b      *Builder
	name   string
	mode   int
	unroll int

	lanes int // 64-bit lanes per vector register
	nvec  int // vector registers per 64-byte stripe

	accA, accB []VReg
	tmp        []VReg // scratch pool, used in groups of three
	kprime     VReg
	// secretRegs holds one whole block's secret schedule, when the register
	// file is big enough for it. See FastStripe.
	secretRegs []VReg
}

func newX86(name string, mode, unroll int) *x86 {
	x := &x86{b: &Builder{}, name: name, mode: mode, unroll: unroll}
	x.lanes = mode / 64
	x.nvec = accNB / x.lanes
	// Accumulators first, then a scratch pool, then the broadcast constant.
	// AVX-512's high sixteen registers have no ABI meaning at all, so the
	// scratch pool moves up there when it exists.
	for i := 0; i < x.nvec; i++ {
		x.accA = append(x.accA, VReg(i))
		x.accB = append(x.accB, VReg(x.nvec+i))
	}
	// The scratch pool is whatever is left below xmm15, in groups of three.
	base, nTmp := 2*x.nvec, 0
	switch mode {
	case modeSSE2:
		nTmp = 6
	case modeAVX2:
		nTmp = 9
	default:
		nTmp = 12
	}
	for i := 0; i < nTmp; i++ {
		x.tmp = append(x.tmp, VReg(base+i))
	}
	x.kprime = VReg(14)
	// AVX-512's upper sixteen registers have no ABI meaning at all, and a
	// stripe costs one register there, so a whole standard block's secret
	// schedule fits in them exactly.
	if mode == modeAVX {
		for i := 0; i < stdBlockStripes; i++ {
			x.secretRegs = append(x.secretRegs, VReg(16+i))
		}
	}
	return x
}

func (x *x86) Name() string     { return x.name }
func (x *x86) GOARCH() string   { return "amd64" }
func (x *x86) Build() *Builder  { return x.b }
func (x *x86) Unroll() int      { return x.unroll }
func (x *x86) SecretImm() bool  { return true }
func (x *x86) ArgGPR(i int) GPR { return x86ArgGPR[i] }

// The vector kernels return nothing and read no table.
func (x *x86) RetGPR() GPR      { return -1 }
func (x *x86) TableGPR() GPR    { return -1 }
func (x *x86) TmpGPR(i int) GPR { return x86TmpGPR[i] }
func (x *x86) GPRName(r GPR) string {
	n, ok := x86GPRNames[r]
	if !ok {
		panic("asmgen: unnamed x86 register")
	}
	return n
}

// vec renders a vector register at this backend's width.
func (x *x86) vec(v VReg) string {
	switch x.mode {
	case modeSSE2:
		return fmt.Sprintf("%%xmm%d", int(v))
	case modeAVX2:
		return fmt.Sprintf("%%ymm%d", int(v))
	default:
		return fmt.Sprintf("%%zmm%d", int(v))
	}
}

func (x *x86) mem(r GPR, off int) string {
	if off == 0 {
		return fmt.Sprintf("(%s)", x.GPRName(r))
	}
	return fmt.Sprintf("%d(%s)", off, x.GPRName(r))
}

// ---------------------------------------------------------------------------
// Integer unit
// ---------------------------------------------------------------------------

func (x *x86) MovRR(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.R[src] },
		"movq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86) MovRI(dst GPR, imm int64) {
	x.b.emit(func(m *Machine) { m.R[dst] = uint64(imm) },
		"movq $%d, %s", imm, x.GPRName(dst))
}

func (x *x86) AddRR(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] += m.R[src] },
		"addq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86) AddRI(dst GPR, imm int64) {
	x.b.emit(func(m *Machine) { m.R[dst] += uint64(imm) },
		"addq $%d, %s", imm, x.GPRName(dst))
}

func (x *x86) SubRR(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] -= m.R[src] },
		"subq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86) SubRI(dst GPR, imm int64) {
	x.b.emit(func(m *Machine) { m.R[dst] -= uint64(imm) },
		"subq $%d, %s", imm, x.GPRName(dst))
}

func (x *x86) ShrRI(dst GPR, sh uint) {
	x.b.emit(func(m *Machine) { m.R[dst] >>= sh },
		"shrq $%d, %s", sh, x.GPRName(dst))
}

func (x *x86) ShlRI(dst GPR, sh uint) {
	x.b.emit(func(m *Machine) { m.R[dst] <<= sh },
		"shlq $%d, %s", sh, x.GPRName(dst))
}

var x86Jcc = map[Cond]string{LT: "jl", GE: "jge", EQ: "je", NE: "jne", GT: "jg", LE: "jle"}

func (x *x86) BranchI(a GPR, imm int64, c Cond, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[a], uint64(imm)) },
		"cmpq $%d, %s", imm, x.GPRName(a))
	x.branch(c, label)
}

func (x *x86) SubBranch(r GPR, imm int64, c Cond, label string) {
	x.b.emit(func(m *Machine) {
		m.setCmp(m.R[r], uint64(imm))
		m.R[r] -= uint64(imm)
	}, "subq $%d, %s", imm, x.GPRName(r))
	x.branch(c, label)
}

func (x *x86) BranchR(a, b GPR, c Cond, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[a], m.R[b]) },
		"cmpq %s, %s", x.GPRName(b), x.GPRName(a))
	x.branch(c, label)
}

func (x *x86) branch(c Cond, label string) {
	x.b.emit(func(m *Machine) {
		if c.eval(m.cmpA, m.cmpB) {
			m.jump(label)
		}
	}, "%s %s", x86Jcc[c], label)
}

func (x *x86) Jmp(label string) {
	x.b.emit(func(m *Machine) { m.jump(label) }, "jmp %s", label)
}

// ---------------------------------------------------------------------------
// Vector unit
// ---------------------------------------------------------------------------
//
// Everything below is written as three-operand `dst = a op b`. SSE2 has only
// two operands, so it lowers to a register copy plus an in-place operation
// where the destination is not already one of the sources. AVX and AVX-512
// take the three-operand form directly, and fold unaligned loads into the
// operation, which SSE2 cannot.

// vecOp emits a commutative three-operand vector operation.
func (x *x86) vecOp(sse, vex, evex string, dst, a, b VReg, sim func(m *Machine, i int)) {
	run := func(m *Machine) {
		for i := 0; i < x.lanes; i++ {
			sim(m, i)
		}
	}
	switch x.mode {
	case modeSSE2:
		switch {
		case dst == a:
		case dst == b:
			a, b = b, a
		default:
			x.vmov(dst, a)
			a = dst
		}
		if dst != a || dst == b {
			panic("asmgen: sse2 " + sse + " needs a distinct destination")
		}
		x.b.emit(run, "%s %s, %s", sse, x.vec(b), x.vec(dst))
	case modeAVX2:
		x.b.emit(run, "%s %s, %s, %s", vex, x.vec(b), x.vec(a), x.vec(dst))
	default:
		x.b.emit(run, "%s %s, %s, %s", evex, x.vec(b), x.vec(a), x.vec(dst))
	}
}

func (x *x86) vmov(dst, src VReg) {
	if dst == src {
		return
	}
	sim := func(m *Machine) { m.V[dst] = m.V[src] }
	switch x.mode {
	case modeSSE2:
		x.b.emit(sim, "movdqa %s, %s", x.vec(src), x.vec(dst))
	case modeAVX2:
		x.b.emit(sim, "vmovdqa %s, %s", x.vec(src), x.vec(dst))
	default:
		x.b.emit(sim, "vmovdqa64 %s, %s", x.vec(src), x.vec(dst))
	}
}

func (x *x86) vload(dst VReg, r GPR, off int) {
	mn := map[int]string{modeSSE2: "movdqu", modeAVX2: "vmovdqu", modeAVX: "vmovdqu64"}[x.mode]
	x.b.emit(func(m *Machine) {
		m.V[dst] = m.LoadVec(m.R[r]+uint64(off), x.lanes)
	}, "%s %s, %s", mn, x.mem(r, off), x.vec(dst))
}

func (x *x86) vstore(src VReg, r GPR, off int) {
	mn := map[int]string{modeSSE2: "movdqu", modeAVX2: "vmovdqu", modeAVX: "vmovdqu64"}[x.mode]
	x.b.emit(func(m *Machine) {
		m.StoreVec(m.R[r]+uint64(off), m.V[src], x.lanes)
	}, "%s %s, %s", mn, x.vec(src), x.mem(r, off))
}

func (x *x86) vxor(dst, a, b VReg) {
	x.vecOp("pxor", "vpxor", "vpxorq", dst, a, b, func(m *Machine, i int) {
		m.V[dst][i] = m.V[a][i] ^ m.V[b][i]
	})
}

func (x *x86) vadd(dst, a, b VReg) {
	x.vecOp("paddq", "vpaddq", "vpaddq", dst, a, b, func(m *Machine, i int) {
		m.V[dst][i] = m.V[a][i] + m.V[b][i]
	})
}

// vmul32 multiplies the low 32 bits of each 64-bit lane, widening to 64.
func (x *x86) vmul32(dst, a, b VReg) {
	x.vecOp("pmuludq", "vpmuludq", "vpmuludq", dst, a, b, func(m *Machine, i int) {
		m.V[dst][i] = uint64(uint32(m.V[a][i])) * uint64(uint32(m.V[b][i]))
	})
}

// vxorMem computes dst = a ^ [r+off]. The secret is not 16-byte aligned and
// advances 8 bytes per stripe, so SSE2 has to load it separately.
func (x *x86) vxorMem(dst, a VReg, r GPR, off int) {
	sim := func(m *Machine) {
		v := m.LoadVec(m.R[r]+uint64(off), x.lanes)
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] = m.V[a][i] ^ v[i]
		}
	}
	switch x.mode {
	case modeSSE2:
		if dst == a {
			panic("asmgen: sse2 vxorMem needs dst != a")
		}
		x.vload(dst, r, off)
		x.b.emit(func(m *Machine) {
			for i := 0; i < x.lanes; i++ {
				m.V[dst][i] ^= m.V[a][i]
			}
		}, "pxor %s, %s", x.vec(a), x.vec(dst))
	case modeAVX2:
		x.b.emit(sim, "vpxor %s, %s, %s", x.mem(r, off), x.vec(a), x.vec(dst))
	default:
		x.b.emit(sim, "vpxorq %s, %s, %s", x.mem(r, off), x.vec(a), x.vec(dst))
	}
}

// vxor3Mem computes dst = dst ^ b ^ [r+off] in one instruction. AVX-512's
// ternary logic takes any three-input bitwise function as a truth table; 0x96
// is the one whose output bit is set where an odd number of inputs are.
func (x *x86) vxor3Mem(dst, b VReg, r GPR, off int) {
	if x.mode != modeAVX {
		panic("asmgen: vxor3Mem needs AVX-512")
	}
	x.b.emit(func(m *Machine) {
		v := m.LoadVec(m.R[r]+uint64(off), x.lanes)
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] ^= m.V[b][i] ^ v[i]
		}
	}, "vpternlogq $0x96, %s, %s, %s", x.mem(r, off), x.vec(b), x.vec(dst))
}

// vmul64 is the full 64-bit lane multiply AVX-512DQ adds. Below it the
// scramble has to assemble the product from two 32x32 halves; see Scramble.
func (x *x86) vmul64(dst, a, b VReg) {
	if x.mode != modeAVX {
		panic("asmgen: vmul64 needs AVX-512DQ")
	}
	x.b.emit(func(m *Machine) {
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] = m.V[a][i] * m.V[b][i]
		}
	}, "vpmullq %s, %s, %s", x.vec(b), x.vec(a), x.vec(dst))
}

func (x *x86) vshr(dst, src VReg, imm uint) {
	sim := func(m *Machine) {
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] = m.V[src][i] >> imm
		}
	}
	if x.mode == modeSSE2 {
		x.vmov(dst, src)
		x.b.emit(sim, "psrlq $%d, %s", imm, x.vec(dst))
		return
	}
	x.b.emit(sim, "vpsrlq $%d, %s, %s", imm, x.vec(src), x.vec(dst))
}

func (x *x86) vshl(dst, src VReg, imm uint) {
	sim := func(m *Machine) {
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] = m.V[src][i] << imm
		}
	}
	if x.mode == modeSSE2 {
		x.vmov(dst, src)
		x.b.emit(sim, "psllq $%d, %s", imm, x.vec(dst))
		return
	}
	x.b.emit(sim, "vpsllq $%d, %s, %s", imm, x.vec(src), x.vec(dst))
}

// vshuf32 permutes the four 32-bit elements of each 128-bit lane by imm.
func (x *x86) vshuf32(dst, src VReg, imm int) {
	sim := func(m *Machine) {
		var out [8]uint64
		for l := 0; l < x.lanes; l += 2 {
			e := [4]uint32{
				uint32(m.V[src][l]), uint32(m.V[src][l] >> 32),
				uint32(m.V[src][l+1]), uint32(m.V[src][l+1] >> 32),
			}
			var o [4]uint32
			for i := 0; i < 4; i++ {
				o[i] = e[(imm>>(2*i))&3]
			}
			out[l] = uint64(o[0]) | uint64(o[1])<<32
			out[l+1] = uint64(o[2]) | uint64(o[3])<<32
		}
		m.V[dst] = out
	}
	mn := "pshufd"
	if x.mode != modeSSE2 {
		mn = "vpshufd"
	}
	x.b.emit(sim, "%s $%#x, %s, %s", mn, imm, x.vec(src), x.vec(dst))
}

func (x *x86) vzero(dst VReg) {
	sim := func(m *Machine) { m.V[dst] = [8]uint64{} }
	switch x.mode {
	case modeSSE2:
		x.b.emit(sim, "pxor %s, %s", x.vec(dst), x.vec(dst))
	case modeAVX2:
		x.b.emit(sim, "vpxor %s, %s, %s", x.vec(dst), x.vec(dst), x.vec(dst))
	default:
		x.b.emit(sim, "vpxorq %s, %s, %s", x.vec(dst), x.vec(dst), x.vec(dst))
	}
}

// hi32 lifts the high half of each 64-bit lane into position for vmul32. On
// SSE2 a shuffle does it in one instruction where a shift would need a copy
// as well; the duplicated halves it leaves in the odd 32-bit slots are never
// read.
func (x *x86) hi32(dst, src VReg) {
	if x.mode == modeSSE2 {
		x.vshuf32(dst, src, 0xf5)
		return
	}
	x.vshr(dst, src, 32)
}

// Setup broadcasts PRIME32_1 for the scramble step, and does nothing at all
// for a kernel that cannot reach one. It clobbers rAX, which the kernel has
// nothing live in at that point.
//
// The two narrower backends want the constant in every 32-bit element, because
// they build the 64-bit product from two 32x32 multiplies; the AVX-512 one has
// a real 64-bit multiply and wants it in every lane.
func (x *x86) Setup(scramble bool) {
	if !scramble {
		return
	}
	// Typed, not untyped: emit takes ...any, which would give an untyped
	// constant its default type of int -- and this one does not fit in an int
	// on a 32-bit host, where the generator and the test suite still have to
	// build even though no kernel here runs there.
	const prime32_1 uint32 = 0x9E3779B1
	x.b.emit(func(m *Machine) { m.R[rAX] = uint64(prime32_1) }, "movl $%d, %%eax", prime32_1)
	bcast32 := func(m *Machine) {
		c := uint64(uint32(m.R[rAX]))
		for i := 0; i < x.lanes; i++ {
			m.V[x.kprime][i] = c | c<<32
		}
	}
	switch x.mode {
	case modeSSE2:
		x.b.emit(func(m *Machine) {}, "movd %%eax, %%xmm%d", int(x.kprime))
		x.b.emit(bcast32, "pshufd $0, %s, %s", x.vec(x.kprime), x.vec(x.kprime))
	case modeAVX2:
		x.b.emit(func(m *Machine) {}, "vmovd %%eax, %%xmm%d", int(x.kprime))
		x.b.emit(bcast32, "vpbroadcastd %%xmm%d, %s", int(x.kprime), x.vec(x.kprime))
	default:
		x.b.emit(func(m *Machine) {
			for i := 0; i < x.lanes; i++ {
				m.V[x.kprime][i] = m.R[rAX]
			}
		}, "vpbroadcastq %%rax, %s", x.vec(x.kprime))
	}
}

// Finish leaves the upper vector state clean, so that legacy SSE code in the
// caller does not pay a transition penalty.
func (x *x86) Finish() {
	if x.mode != modeSSE2 {
		x.b.emit(func(m *Machine) {}, "vzeroupper")
	}
}

// LoadAcc reads the accumulators in 128-bit pieces rather than in one go.
//
// They arrive from Go, and the two places they come from -- a copy of initAcc
// on the long path, a Digest field on the streaming one -- are both written by
// the compiler as 16-byte moves, because that is all the amd64 baseline has. A
// 32- or 64-byte load spanning several of those stores cannot take its data
// from the store queue: it waits for them to reach the cache. Loading in the
// width they were written in forwards instead, and is worth 26% of a 256-byte
// hash and 15% of a 1 KiB one on a Zen 4.
func (x *x86) LoadAcc(p GPR) {
	w := x.mode / 8
	for i := 0; i < x.nvec; i++ {
		x.vloadPieces(x.accA[i], p, w*i)
		x.vzero(x.accB[i])
	}
}

func (x *x86) vloadPieces(dst VReg, r GPR, off int) {
	if x.mode == modeSSE2 {
		x.vload(dst, r, off)
		return
	}
	x.b.emit(func(m *Machine) {
		var v [8]uint64
		v[0] = m.Load64(m.R[r] + uint64(off))
		v[1] = m.Load64(m.R[r] + uint64(off+8))
		m.V[dst] = v
	}, "vmovdqu %s, %%xmm%d", x.mem(r, off), int(dst))
	mn := "vinserti128"
	if x.mode == modeAVX {
		mn = "vinserti64x2"
	}
	for i := 1; i < x.lanes/2; i++ {
		i := i
		x.b.emit(func(m *Machine) {
			m.V[dst][2*i] = m.Load64(m.R[r] + uint64(off+16*i))
			m.V[dst][2*i+1] = m.Load64(m.R[r] + uint64(off+16*i+8))
		}, "%s $%d, %s, %s, %s", mn, i, x.mem(r, off+16*i), x.vec(dst), x.vec(dst))
	}
}

func (x *x86) StoreAcc(p GPR) {
	w := x.mode / 8
	for i := 0; i < x.nvec; i++ {
		x.vstorePieces(x.accA[i], p, w*i)
	}
}

func (x *x86) vstorePieces(src VReg, r GPR, off int) {
	if x.mode == modeSSE2 {
		x.vstore(src, r, off)
		return
	}
	x.b.emit(func(m *Machine) { m.StoreVec(m.R[r]+uint64(off), m.V[src], 2) },
		"vmovdqu %%xmm%d, %s", int(src), x.mem(r, off))
	mn := "vextracti128"
	if x.mode == modeAVX {
		mn = "vextracti64x2"
	}
	for i := 1; i < x.lanes/2; i++ {
		i := i
		x.b.emit(func(m *Machine) {
			m.Store64(m.R[r]+uint64(off+16*i), m.V[src][2*i])
			m.Store64(m.R[r]+uint64(off+16*i+8), m.V[src][2*i+1])
		}, "%s $%d, %s, %s", mn, i, x.vec(src), x.mem(r, off+16*i))
	}
}

// Stripe absorbs 64 bytes: each vector's keyed 32x32 product goes to accA and
// its raw input to accB. The lane swap the algorithm applies to the raw input
// is deferred to Materialize, which is what keeps this to five vector
// operations per register instead of six.
// GroupBegin has nothing to hoist here: the secret is read straight out of
// memory by the xor that consumes it.
func (x *x86) GroupBegin(GPR) {}

func (x *x86) Stripe(k int, in GPR, inOff int, sec GPR, secOff int) {
	w := x.mode / 8
	if k < 0 {
		k = 0
	}
	for j := 0; j < x.nvec; j++ {
		t := x.tmp[3*((k*x.nvec+j)%(len(x.tmp)/3)):]
		d, key, h := t[0], t[1], t[2]
		x.vload(d, in, inOff+w*j)
		x.vxorMem(key, d, sec, secOff+w*j)
		x.hi32(h, key)
		x.vmul32(key, key, h)
		x.vadd(x.accA[j], x.accA[j], key)
		x.vadd(x.accB[j], x.accB[j], d)
	}
}

func (x *x86) FastBlockStripes() int { return len(x.secretRegs) }

func (x *x86) LoadSecretRegs(sec GPR) {
	for k, r := range x.secretRegs {
		x.vload(r, sec, secretConsumeRate*k)
	}
}

// FastStripe is Stripe with the stripe's secret already in a register. The
// arithmetic is unchanged; what it saves is the secret's load.
//
// A stripe touches two memory operands, the input and the secret, and needs
// the input twice: once keyed, once raw. So one of the two has to be loaded
// into a register and only the other can be folded into an operation. Holding
// the secret makes that one free.
//
// It is tempting to go further and fold the input into both operations
// instead, which removes the load instruction altogether and leaves five
// instructions per stripe rather than six. That measured 1.9% *slower* on a
// Zen 4: an AMD core cracks a folded memory operand into a load and an
// operation regardless, so nothing is saved, and the two dependent operations
// sit in the floating-point scheduler until their loads return -- the
// scheduler-full stall went from nil to 17% of cycles.
func (x *x86) FastStripe(k int, in GPR, inOff int) {
	if x.nvec != 1 {
		panic("asmgen: FastStripe assumes one register per stripe")
	}
	t := x.tmp[3*(k%(len(x.tmp)/3)):]
	d, key, h := t[0], t[1], t[2]
	x.vload(d, in, inOff)
	x.vxor(key, d, x.secretRegs[k])
	x.hi32(h, key)
	x.vmul32(key, key, h)
	x.vadd(x.accA[0], x.accA[0], key)
	x.vadd(x.accB[0], x.accB[0], d)
}

// Materialize folds the deferred lane swap in: acc = accA + swap(accB).
func (x *x86) Materialize(final bool) {
	t := x.tmp[0]
	for j := 0; j < x.nvec; j++ {
		x.vshuf32(t, x.accB[j], 0x4e)
		x.vadd(x.accA[j], x.accA[j], t)
		if !final {
			x.vzero(x.accB[j])
		}
	}
}

// Scramble applies acc = (xorshift(acc,47) ^ secret) * PRIME32_1. Below
// AVX-512DQ there is no 64-bit vector multiply, so the product is assembled
// from the two 32x32 halves.
//
// This runs once per block against a single register, so it is a bare
// dependency chain with no other work of its own to hide the latency: what
// each link costs is what it costs. AVX-512 spends three -- shift, a ternary
// xor that folds both the xorshift and the secret into one instruction, and
// the multiply -- against the six the narrower backends need.
func (x *x86) Scramble(sec GPR, secOff int) {
	w := x.mode / 8
	for j := 0; j < x.nvec; j++ {
		a := x.accA[j]
		t, hi, lo := x.tmp[0], x.tmp[1], x.tmp[2]
		x.vshr(t, a, 47)
		if x.mode == modeAVX {
			x.vxor3Mem(a, t, sec, secOff+w*j)
			x.vmul64(a, a, x.kprime)
			continue
		}
		x.vxor(a, a, t)
		x.vxorMem(t, a, sec, secOff+w*j)
		x.hi32(hi, t)
		x.vmul32(lo, t, x.kprime)
		x.vmul32(hi, hi, x.kprime)
		x.vshl(hi, hi, 32)
		x.vadd(a, lo, hi)
	}
}

const accNB = 8
