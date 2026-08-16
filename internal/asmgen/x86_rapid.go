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
// BMI2's mulx lifts the constraint: it takes the multiplicand in rdx
// implicitly and writes two registers of its choosing, so a round needs no
// move to get the result out of rax. That is one instruction in six, in a
// loop this core spends 78% of its slots retiring -- so instructions are what
// it costs. The block loop is emitted twice for that reason and the form is
// chosen at run time from secret[8].
//
// The branch is not at the top of the kernel. It sits after the n > 112 test,
// so only an input long enough to run the block loop pays it, and there it is
// one predictable branch against fourteen multiplies an iteration. Putting a
// form test in front of every call is what the XXH64 kernel does, and it
// costs 3-7% of a short hash there; this hash is shorter still.
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

// The indexed loads read at base+idx+off in one instruction. Every "read at
// n-k" the short paths do is that addressing mode, which is why none of them
// computes the index in a register first: mov, sub and a load is three
// instructions where disp(base,index,1) is one, and at these lengths the hash
// is bound by nothing but how many instructions it is.
func (x *x86Rapid) loadIdx8Off(dst, base, idx GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load8(m.R[base] + m.R[idx] + uint64(int64(off))) },
		"movzbq %d(%s,%s,1), %s", off, x.GPRName(base), x.GPRName(idx), x.GPRName(dst))
}

func (x *x86Rapid) loadIdx8(dst, base, idx GPR) { x.loadIdx8Off(dst, base, idx, 0) }

func (x *x86Rapid) loadIdx32Off(dst, base, idx GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load32(m.R[base] + m.R[idx] + uint64(int64(off))) },
		"movl %d(%s,%s,1), %s", off, x.GPRName(base), x.GPRName(idx), x.GPRName32(dst))
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

func (x *x86Rapid) Round(lane GPR, off, slot int, mulx bool) {
	// lane = mix(load(in+off) ^ secret[slot], load(in+off+8) ^ lane)
	//
	// The second operand is built in r14, the one scratch register the plan
	// keeps free, and the secret is an operand of the xor rather than a load
	// of its own.
	if mulx {
		// mulx names both destinations, so the low half lands in the lane
		// directly and the high half goes back into r14 -- which mulx may do
		// even though r14 is also its source, since the source is read before
		// either destination is written. Six instructions against seven.
		x.load64(rDX, x.In(), off)
		x.xorSec(rDX, slot)
		x.load64(r14, x.In(), off+8)
		x.xor(r14, lane)
		x.mulx(r14, lane, r14)
		x.xor(lane, r14)
		return
	}
	// The first operand goes to rax because mulq demands it.
	x.load64(rAX, x.In(), off)
	x.xorSec(rAX, slot)
	x.load64(r14, x.In(), off+8)
	x.xor(r14, lane)
	x.mixInto(lane, r14)
}

// mulx is BMI2's unsigned multiply: hi:lo = rdx * src, with both destinations
// named. src may be the same register as hi.
func (x *x86Rapid) mulx(src, lo, hi GPR) {
	x.b.emit(func(m *Machine) {
		a, b := m.R[rDX], m.R[src]
		l, h := a*b, mulHigh(a, b)
		m.R[lo], m.R[hi] = l, h
	}, "mulxq %s, %s, %s", x.GPRName(src), x.GPRName(lo), x.GPRName(hi))
}

// DualMul is on: see the file comment.
func (x *x86Rapid) DualMul() bool { return true }

// AltBlockBody is the two-group iteration, lane by lane rather than group by
// group, with each lane's secret word loaded once into rax and used by both
// of that lane's rounds.
//
// rax is free here and nowhere else: mulq's fixed destination is what
// normally occupies it, so this shape exists only because the form already
// uses mulx. That is where most of the alternative form's win is. The
// shipped order loads all seven secret words twice per iteration -- fourteen
// L1 loads that the loop is measurably short of ports for. Dropping the
// secret load entirely, which nothing can, is worth 12.7% of the loop;
// halving it this way is worth 6.7% on top of mulx alone, and mulx alone is
// 10.8%.
//
// Reordering is safe: a lane's two rounds keep their order, and no lane
// reads another. The bits are identical, which the simulator checks by
// running every length through both forms.
func (x *x86Rapid) AltBlockBody(lane func(int) GPR) {
	for i := 0; i < 7; i++ {
		x.movSec(rAX, i)
		x.roundHeldSecret(lane(i), i*16, rAX)
		x.roundHeldSecret(lane(i), 112+i*16, rAX)
	}
}

// roundHeldSecret is the mulx round with the secret word already in a
// register rather than a memory operand of the xor.
func (x *x86Rapid) roundHeldSecret(lane GPR, off int, sec GPR) {
	x.load64(rDX, x.In(), off)
	x.xor(rDX, sec)
	x.load64(r14, x.In(), off+8)
	x.xor(r14, lane)
	x.mulx(r14, lane, r14)
	x.xor(lane, r14)
}

// BranchNotAltMul branches to label when secret[8] says the machine has no
// BMI2. The flag is in the table because a generated body cannot name a Go
// symbol, and because the kernel is holding that pointer anyway.
func (x *x86Rapid) BranchNotAltMul(label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.Load64(m.R[rCX]+64), 0) },
		"cmpq $0, %s", x.mem(rCX, 64))
	x.branch(EQ, label)
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
	x.loadIdx32Off(rDX, x.In(), x.I(), -4)
	x.Jmp(done)
	x.b.Label(eight)
	// 8..16: 64-bit reads at 0 and at n-8, overlapping below 16.
	x.load64(rAX, x.In(), 0)
	x.loadIdx64Off(rDX, x.In(), x.I(), -8)
	x.b.Label(done)
}

func (x *x86Rapid) Short1to3() {
	// a = p[0]<<45 | p[n-1]; b = p[n>>1]
	x.load8(rAX, x.In(), 0)
	x.shl(rAX, 45)
	x.loadIdx8Off(rDX, x.In(), x.I(), -1)
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
