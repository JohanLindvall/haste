package asmgen

// The rapidhash kernel on x86-64.
//
// Register plan, in the thirteen the Go ABI leaves an ABI0 leaf:
//
//	rax mul low / a    rdx mul high      rdi in       rsi i
//	rcx secret table   rbx seed/lane0    r8..r13 see1..see6
//	r14 scratch
//
// mulq is the constraint the whole plan is built around: it multiplies rax by
// its operand and writes rdx:rax, so both are spoken for at every multiply
// and neither can hold anything that has to live across one. That is why the
// seed is in rbx rather than rdx, where the third argument would naturally
// land, and why a round is a move, a mulq and a xor rather than arm64's two
// independent multiplies.
//
// BMI2's mulx would lift the constraint -- it takes the multiplicand in rdx
// implicitly and writes two registers of its choosing, so the seven lanes
// would not queue through one pair. It is not in the amd64 baseline, so
// taking it means a second kernel and a CPUID check, the way the XXH64
// backend carries two prime forms. Worth doing on evidence; not worth doing
// before there is any, and the lanes are only seven multiplies deep in a loop
// that is otherwise load-bound.
//
// The secret is reached through a pointer. Eight 64-bit constants would be
// ten bytes each as immediates, eighty bytes of prologue every call pays
// against one lea and a memory operand per use -- and the x86 rounds can take
// the secret word as the xor's operand directly, which is one instruction
// where arm64 needs a load first.

type x86Rapid struct {
	*x86
}

func newX86Rapid() *x86Rapid {
	return &x86Rapid{x86: &x86{b: &Builder{}, name: "rapid"}}
}

// GPRName32 is the 32-bit view of a register, which movl writes and which
// zero-extends to 64 bits on x86-64 -- that zero extension is what makes the
// 4..7 path's two 32-bit reads correct without an explicit widening.
func (x *x86Rapid) GPRName32(r GPR) string {
	return map[GPR]string{
		rAX: "%eax", rCX: "%ecx", rDX: "%edx", rBX: "%ebx",
		rSI: "%esi", rDI: "%edi", r8: "%r8d", r9: "%r9d",
		r10: "%r10d", r11: "%r11d", r12: "%r12d", r13: "%r13d", r14: "%r14d",
	}[r]
}

func (x *x86Rapid) Name() string    { return "rapid" }
func (x *x86Rapid) GOARCH() string  { return "amd64" }
func (x *x86Rapid) Build() *Builder { return x.b }

// The arguments land where the plan wants them: in and i in rdi and rsi, and
// the seed in rbx, which mulq will not disturb.
func (x *x86Rapid) ArgGPR(i int) GPR { return [...]GPR{rDI, rSI, rBX}[i] }
func (x *x86Rapid) RetGPR() GPR      { return rAX }
func (x *x86Rapid) TableGPR() GPR    { return rCX }

func (x *x86Rapid) In() GPR   { return rDI }
func (x *x86Rapid) I() GPR    { return rSI }
func (x *x86Rapid) Seed() GPR { return rBX }
func (x *x86Rapid) See(i int) GPR {
	if i < 1 || i > 6 {
		panic("asmgen: rapid lane out of range")
	}
	return [...]GPR{0, r8, r9, r10, r11, r12, r13}[i]
}
func (x *x86Rapid) A() GPR   { return rAX }
func (x *x86Rapid) B() GPR   { return rDX }
func (x *x86Rapid) tmp() GPR { return r14 }

// sec is a memory operand for secret[slot], off the table pointer.
func (x *x86Rapid) sec(slot int) string { return x.mem(rCX, 8*slot) }

// mulRAX is mulq: rdx:rax = rax * src. Every multiply in this kernel goes
// through it, which is why nothing long-lived lives in rax or rdx.
func (x *x86Rapid) mulRAX(src GPR) {
	x.b.emit(func(m *Machine) {
		lo, hi := m.R[rAX]*m.R[src], mulHigh(m.R[rAX], m.R[src])
		m.R[rAX], m.R[rDX] = lo, hi
	}, "mulq %s", x.GPRName(src))
}

// mixInto leaves lo^hi of rax*src in dst. rax must already hold one operand.
func (x *x86Rapid) mixInto(dst, src GPR) {
	x.mulRAX(src)
	x.xor(rAX, rDX)
	if dst != rAX {
		x.mov(dst, rAX)
	}
}

func (x *x86Rapid) mov(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.R[src] },
		"movq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86Rapid) xor(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] ^= m.R[src] },
		"xorq %s, %s", x.GPRName(src), x.GPRName(dst))
}

// xorSec is dst ^= secret[slot], the secret straight out of memory.
func (x *x86Rapid) xorSec(dst GPR, slot int) {
	x.b.emit(func(m *Machine) { m.R[dst] ^= m.Load64(m.R[rCX] + uint64(8*slot)) },
		"xorq %s, %s", x.sec(slot), x.GPRName(dst))
}

func (x *x86Rapid) movSec(dst GPR, slot int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[rCX] + uint64(8*slot)) },
		"movq %s, %s", x.sec(slot), x.GPRName(dst))
}

func (x *x86Rapid) load64(dst, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + uint64(off)) },
		"movq %s, %s", x.mem(base, off), x.GPRName(dst))
}

func (x *x86Rapid) load32(dst, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load32(m.R[base] + uint64(off)) },
		"movl %s, %s", x.mem(base, off), x.GPRName32(dst))
}

func (x *x86Rapid) load8(dst, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load8(m.R[base] + uint64(off)) },
		"movzbq %s, %s", x.mem(base, off), x.GPRName(dst))
}

// loadIdx8 is a byte load at base+idx, which the 1..3 path needs: neither of
// its offsets is a constant.
func (x *x86Rapid) loadIdx8(dst, base, idx GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load8(m.R[base] + m.R[idx]) },
		"movzbq (%s,%s,1), %s", x.GPRName(base), x.GPRName(idx), x.GPRName(dst))
}

func (x *x86Rapid) loadIdx32(dst, base, idx GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load32(m.R[base] + m.R[idx]) },
		"movl (%s,%s,1), %s", x.GPRName(base), x.GPRName(idx), x.GPRName32(dst))
}

func (x *x86Rapid) loadIdx64(dst, base, idx GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + m.R[idx]) },
		"movq (%s,%s,1), %s", x.GPRName(base), x.GPRName(idx), x.GPRName(dst))
}

// loadIdx64Off is a load at base+idx+off, for the tail's In + I - 16.
func (x *x86Rapid) loadIdx64Off(dst, base, idx GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + m.R[idx] + uint64(int64(off))) },
		"movq %d(%s,%s,1), %s", off, x.GPRName(base), x.GPRName(idx), x.GPRName(dst))
}

func (x *x86Rapid) addImm(dst GPR, imm int64) {
	x.b.emit(func(m *Machine) { m.R[dst] += uint64(imm) },
		"addq $%d, %s", imm, x.GPRName(dst))
}

func (x *x86Rapid) subImm(dst GPR, imm int64) {
	x.b.emit(func(m *Machine) { m.R[dst] -= uint64(imm) },
		"subq $%d, %s", imm, x.GPRName(dst))
}

func (x *x86Rapid) shl(dst GPR, sh uint) {
	x.b.emit(func(m *Machine) { m.R[dst] <<= sh },
		"shlq $%d, %s", sh, x.GPRName(dst))
}

func (x *x86Rapid) shr(dst GPR, sh uint) {
	x.b.emit(func(m *Machine) { m.R[dst] >>= sh },
		"shrq $%d, %s", sh, x.GPRName(dst))
}

func (x *x86Rapid) or(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] |= m.R[src] },
		"orq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86Rapid) Zero(dst GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = 0 },
		"xorq %s, %s", x.GPRName(dst), x.GPRName(dst))
}

// ---------------------------------------------------------------------------
// The RapidArch surface
// ---------------------------------------------------------------------------

func (x *x86Rapid) SeedMix() {
	// seed ^= mix(seed ^ secret[2], secret[1])
	x.mov(rAX, x.Seed())
	x.xorSec(rAX, 2)
	x.movSec(r14, 1)
	x.mixInto(rAX, r14)
	x.xor(x.Seed(), rAX)
}

// SeedConst is SeedMix with the seed known to be zero: one load of the ninth
// secret word, where the value it always computes is kept.
func (x *x86Rapid) SeedConst() { x.movSec(x.Seed(), 8) }

func (x *x86Rapid) Round(lane GPR, off, slot int) {
	// lane = mix(load(in+off) ^ secret[slot], load(in+off+8) ^ lane)
	//
	// The first operand goes to rax because mulq demands it; the second is
	// built in r14, the one scratch register the plan keeps free. The secret
	// is an operand of the xor rather than a load of its own.
	x.load64(rAX, x.In(), off)
	x.xorSec(rAX, slot)
	x.load64(r14, x.In(), off+8)
	x.xor(r14, lane)
	x.mixInto(lane, r14)
}

func (x *x86Rapid) SpreadLanes() {
	for i := 1; i <= 6; i++ {
		x.mov(x.See(i), x.Seed())
	}
}

func (x *x86Rapid) Converge() {
	x.xor(x.Seed(), x.See(1))
	x.xor(x.See(2), x.See(3))
	x.xor(x.See(4), x.See(5))
	x.xor(x.Seed(), x.See(6))
	x.xor(x.See(2), x.See(4))
	x.xor(x.Seed(), x.See(2))
}

func (x *x86Rapid) Short4to16() {
	eight, done := x.b.NewLabel("eight"), x.b.NewLabel("shortdone")
	x.xor(x.Seed(), x.I())
	x.BranchI(x.I(), 8, GE, eight)
	// 4..7: 32-bit reads at 0 and at n-4.
	x.load32(rAX, x.In(), 0)
	x.mov(r14, x.I())
	x.subImm(r14, 4)
	x.loadIdx32(rDX, x.In(), r14)
	x.Jmp(done)
	x.b.Label(eight)
	// 8..16: 64-bit reads at 0 and at n-8, overlapping below 16.
	x.load64(rAX, x.In(), 0)
	x.mov(r14, x.I())
	x.subImm(r14, 8)
	x.loadIdx64(rDX, x.In(), r14)
	x.b.Label(done)
}

func (x *x86Rapid) Short1to3() {
	// a = p[0]<<45 | p[n-1]; b = p[n>>1]
	x.load8(rAX, x.In(), 0)
	x.shl(rAX, 45)
	x.mov(r14, x.I())
	x.subImm(r14, 1)
	x.loadIdx8(rDX, x.In(), r14)
	x.or(rAX, rDX)
	x.mov(r14, x.I())
	x.shr(r14, 1)
	x.loadIdx8(rDX, x.In(), r14)
}

func (x *x86Rapid) Tail16() {
	// a = load(in + i - 16) ^ i; b = load(in + i - 8)
	x.loadIdx64Off(rAX, x.In(), x.I(), -16)
	x.loadIdx64Off(rDX, x.In(), x.I(), -8)
	x.xor(rAX, x.I())
}

func (x *x86Rapid) Finalize() {
	// a ^= secret[1]; b ^= seed; a, b = mum(a, b)
	// ret = mix(a ^ secret[7], b ^ secret[1] ^ i)
	x.xorSec(rAX, 1)
	x.xor(rDX, x.Seed())
	// mum leaves both halves, and both are wanted, so the product is taken
	// apart before either is used. r14 and a dead lane hold them.
	x.mulRAX(rDX)
	lo, hi := r14, r8 // r8 is see1, dead once Converge has run
	x.mov(lo, rAX)
	x.mov(hi, rDX)
	x.xorSec(lo, 7)
	x.xorSec(hi, 1)
	x.xor(hi, x.I())
	x.mov(rAX, lo)
	x.mixInto(rAX, hi)
}

func (x *x86Rapid) AdvanceIn(bytes int) { x.addImm(x.In(), int64(bytes)) }
func (x *x86Rapid) SubI(bytes int)      { x.subImm(x.I(), int64(bytes)) }
