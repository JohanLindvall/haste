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
)

var x86GPRNames = map[GPR]string{
	rAX: "%rax", rCX: "%rcx", rDX: "%rdx", rBX: "%rbx",
	rSI: "%rsi", rDI: "%rdi", r8: "%r8", r9: "%r9",
	r10: "%r10", r11: "%r11", r12: "%r12", r13: "%r13",
}

// Go's amd64 ABI keeps R14 for the goroutine pointer, R15 for dynamic linking
// and X15 as a zero register, so none of them appear above or in the vector
// pools below.
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
	// The scratch pool is whatever is left below xmm15, in groups of three;
	// AVX-512's upper sixteen registers have no ABI meaning at all, so that
	// is where its pool goes.
	base, nTmp := 2*x.nvec, 0
	switch mode {
	case modeSSE2:
		nTmp = 6
	case modeAVX2:
		nTmp = 9
	default:
		base, nTmp = 16, 12
	}
	for i := 0; i < nTmp; i++ {
		x.tmp = append(x.tmp, VReg(base+i))
	}
	x.kprime = VReg(14)
	return x
}

func (x *x86) Name() string     { return x.name }
func (x *x86) GOARCH() string   { return "amd64" }
func (x *x86) Build() *Builder  { return x.b }
func (x *x86) Unroll() int      { return x.unroll }
func (x *x86) SecretImm() bool  { return true }
func (x *x86) ArgGPR(i int) GPR { return x86ArgGPR[i] }
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
	bcast := func(m *Machine) {
		c := uint64(uint32(m.R[rAX]))
		for i := 0; i < x.lanes; i++ {
			m.V[x.kprime][i] = c | c<<32
		}
	}
	switch x.mode {
	case modeSSE2:
		x.b.emit(func(m *Machine) {}, "movd %%eax, %%xmm%d", int(x.kprime))
		x.b.emit(bcast, "pshufd $0, %s, %s", x.vec(x.kprime), x.vec(x.kprime))
	case modeAVX2:
		x.b.emit(func(m *Machine) {}, "vmovd %%eax, %%xmm%d", int(x.kprime))
		x.b.emit(bcast, "vpbroadcastd %%xmm%d, %s", int(x.kprime), x.vec(x.kprime))
	default:
		x.b.emit(bcast, "vpbroadcastd %%eax, %s", x.vec(x.kprime))
	}
}

// Finish leaves the upper vector state clean, so that legacy SSE code in the
// caller does not pay a transition penalty.
func (x *x86) Finish() {
	if x.mode != modeSSE2 {
		x.b.emit(func(m *Machine) {}, "vzeroupper")
	}
}

func (x *x86) LoadAcc(p GPR) {
	w := x.mode / 8
	for i := 0; i < x.nvec; i++ {
		x.vload(x.accA[i], p, w*i)
		x.vzero(x.accB[i])
	}
}

func (x *x86) StoreAcc(p GPR) {
	w := x.mode / 8
	for i := 0; i < x.nvec; i++ {
		x.vstore(x.accA[i], p, w*i)
	}
}

// Stripe absorbs 64 bytes: each vector's keyed 32x32 product goes to accA and
// its raw input to accB. The lane swap the algorithm applies to the raw input
// is deferred to Materialize, which is what keeps this to five vector
// operations per register instead of six.
func (x *x86) Stripe(k int, in GPR, inOff int, sec GPR, secOff int) {
	w := x.mode / 8
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
func (x *x86) Scramble(sec GPR, secOff int) {
	w := x.mode / 8
	for j := 0; j < x.nvec; j++ {
		a := x.accA[j]
		t, hi, lo := x.tmp[0], x.tmp[1], x.tmp[2]
		x.vshr(t, a, 47)
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
