package asmgen

import "fmt"

// The rapidhash kernel on arm64.
//
// Register plan, all caller-saved and none of them the platform's:
//
//	x0 in      x1 i        x2 seed/lane0   x3 secret table
//	x4..x9 see1..see6      x10 a           x11 b
//	x12 scratch (a loaded word)            x13 scratch (the high half)
//
// mul and umulh are three-operand, so a round needs no register shuffling:
// the two halves land where they are wanted and are xored together. The pair
// costs two instructions where x86 needs a move, a mulq and a xor, and the
// two multiplies are independent of each other, which the wide cores issue
// together.
//
// The secret is read through a pointer rather than materialized. Its eight
// words would be four instructions each as immediates -- movz plus three
// movk -- against one ldr from a line the kernel is already touching.

type arm64Rapid struct {
	*arm64
}

func newARM64Rapid() *arm64Rapid {
	return &arm64Rapid{arm64: &arm64{b: &Builder{}, name: "rapid"}}
}

// The kernel takes (in, n, seed) in x0..x2 and returns in x0. Name, GOARCH,
// Build and ArgGPR come from the embedded arm64.

// wname is the 32-bit view of a register, which the sub-word loads write.
func (a *arm64Rapid) wname(r GPR) string { return fmt.Sprintf("w%d", int(r)) }
func (a *arm64Rapid) RetGPR() GPR        { return GPR(0) }
func (a *arm64Rapid) TableGPR() GPR      { return GPR(3) }

func (a *arm64Rapid) In() GPR   { return GPR(0) }
func (a *arm64Rapid) I() GPR    { return GPR(1) }
func (a *arm64Rapid) Seed() GPR { return GPR(2) }
func (a *arm64Rapid) sec() GPR  { return GPR(3) }
func (a *arm64Rapid) See(i int) GPR {
	if i < 1 || i > 6 {
		panic(fmt.Sprintf("asmgen: rapid lane %d out of range", i))
	}
	return GPR(3 + i) // x4..x9
}
func (a *arm64Rapid) A() GPR    { return GPR(10) }
func (a *arm64Rapid) B() GPR    { return GPR(11) }
func (a *arm64Rapid) tmp() GPR  { return GPR(12) }
func (a *arm64Rapid) tmp2() GPR { return GPR(13) }

// mix leaves lo^hi of x*y in dst. dst may be either source.
func (a *arm64Rapid) mix(dst, x, y GPR) {
	hi := a.tmp2()
	a.umulh(hi, x, y)
	a.mul(dst, x, y)
	a.eor(dst, dst, hi)
}

func (a *arm64Rapid) mul(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] * m.R[y] },
		"mul %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64Rapid) umulh(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = mulHigh(m.R[x], m.R[y]) },
		"umulh %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64Rapid) eor(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] ^ m.R[y] },
		"eor %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64Rapid) ldrSecret(dst GPR, slot int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[a.sec()] + uint64(8*slot)) },
		"ldr %s, [%s, #%d]", a.GPRName(dst), a.GPRName(a.sec()), 8*slot)
}

func (a *arm64Rapid) ldr(dst, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + uint64(off)) },
		"ldr %s, [%s, #%d]", a.GPRName(dst), a.GPRName(base), off)
}

// ldp loads two consecutive words in one instruction, which is what a round's
// pair of loads is.
func (a *arm64Rapid) ldp(d0, d1, base GPR, off int) {
	a.b.emit(func(m *Machine) {
		m.R[d0] = m.Load64(m.R[base] + uint64(off))
		m.R[d1] = m.Load64(m.R[base] + uint64(off) + 8)
	}, "ldp %s, %s, [%s, #%d]", a.GPRName(d0), a.GPRName(d1), a.GPRName(base), off)
}

func (a *arm64Rapid) ldrw(dst, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = uint64(m.Load32(m.R[base] + uint64(off))) },
		"ldr %s, [%s, #%d]", a.wname(dst), a.GPRName(base), off)
}

func (a *arm64Rapid) ldrb(dst, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = uint64(m.Load8(m.R[base] + uint64(off))) },
		"ldrb %s, [%s, #%d]", a.wname(dst), a.GPRName(base), off)
}

// ldrbReg is ldrb with a register offset, which the 1..3 path needs: the byte
// it wants is at n-1 and at n>>1, neither of them a constant.
func (a *arm64Rapid) ldrbReg(dst, base, off GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = uint64(m.Load8(m.R[base] + m.R[off])) },
		"ldrb %s, [%s, %s]", a.wname(dst), a.GPRName(base), a.GPRName(off))
}

// ldrReg is ldr with a register offset, for the tail's In+I-16.
func (a *arm64Rapid) ldrReg(dst, base, off GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + m.R[off]) },
		"ldr %s, [%s, %s]", a.GPRName(dst), a.GPRName(base), a.GPRName(off))
}

func (a *arm64Rapid) add(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] + m.R[y] },
		"add %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64Rapid) subImm(dst GPR, imm int64) {
	a.b.emit(func(m *Machine) { m.R[dst] -= uint64(imm) },
		"sub %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), imm)
}

func (a *arm64Rapid) addImm(dst GPR, imm int64) {
	a.b.emit(func(m *Machine) { m.R[dst] += uint64(imm) },
		"add %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), imm)
}

func (a *arm64Rapid) lsl(dst, src GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] << sh },
		"lsl %s, %s, #%d", a.GPRName(dst), a.GPRName(src), sh)
}

func (a *arm64Rapid) lsr(dst, src GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] >> sh },
		"lsr %s, %s, #%d", a.GPRName(dst), a.GPRName(src), sh)
}

func (a *arm64Rapid) orr(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] | m.R[y] },
		"orr %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64Rapid) mov(dst, src GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] },
		"mov %s, %s", a.GPRName(dst), a.GPRName(src))
}

func (a *arm64Rapid) Zero(dst GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = 0 }, "mov %s, xzr", a.GPRName(dst))
}

// ---------------------------------------------------------------------------
// The RapidArch surface
// ---------------------------------------------------------------------------

func (a *arm64Rapid) SeedMix() {
	// seed ^= mix(seed ^ secret[2], secret[1])
	t, s1 := a.tmp(), a.A()
	a.ldrSecret(t, 2)
	a.eor(t, t, a.Seed())
	a.ldrSecret(s1, 1)
	a.mix(t, t, s1)
	a.eor(a.Seed(), a.Seed(), t)
}

func (a *arm64Rapid) Round(lane GPR, off, slot int, _ bool) {
	// lane = mix(load(in+off) ^ secret[slot], load(in+off+8) ^ lane)
	w0, w1 := a.tmp(), a.A()
	a.ldp(w0, w1, a.In(), off)
	s := a.B()
	a.ldrSecret(s, slot)
	a.eor(w0, w0, s)
	a.eor(w1, w1, lane)
	a.mix(lane, w0, w1)
}

// DualMul is off: mul and umulh are three-operand and unconstrained, so
// there is no second form to choose between.
func (a *arm64Rapid) DualMul() bool { return false }

// BranchNotAltMul and AltBlockBody are unreachable with DualMul off.
func (a *arm64Rapid) BranchNotAltMul(string) { panic("asmgen: arm64 rapid has one multiply form") }
func (a *arm64Rapid) AltBlockBody(func(int) GPR) {
	panic("asmgen: arm64 rapid has one block-loop form")
}

func (a *arm64Rapid) SpreadLanes() {
	for i := 1; i <= 6; i++ {
		a.mov(a.See(i), a.Seed())
	}
}

func (a *arm64Rapid) Converge() {
	// seed^=see1; see2^=see3; see4^=see5; seed^=see6; see2^=see4; seed^=see2
	a.eor(a.Seed(), a.Seed(), a.See(1))
	a.eor(a.See(2), a.See(2), a.See(3))
	a.eor(a.See(4), a.See(4), a.See(5))
	a.eor(a.Seed(), a.Seed(), a.See(6))
	a.eor(a.See(2), a.See(2), a.See(4))
	a.eor(a.Seed(), a.Seed(), a.See(2))
}

func (a *arm64Rapid) Short4to16() {
	// seed ^= n, then a and b from the two ends, 64-bit at 8 and up.
	eight := a.b.NewLabel("eight")
	doneS := a.b.NewLabel("shortdone")
	a.eor(a.Seed(), a.Seed(), a.I())
	a.BranchI(a.I(), 8, GE, eight)
	// 4..7: two 32-bit reads, at 0 and at n-4.
	a.ldrw(a.A(), a.In(), 0)
	t := a.tmp()
	a.mov(t, a.I())
	a.subImm(t, 4)
	a.ldrwReg(a.B(), a.In(), t)
	a.Jmp(doneS)
	a.b.Label(eight)
	// 8..16: two 64-bit reads, at 0 and at n-8, overlapping below 16.
	a.ldr(a.A(), a.In(), 0)
	t2 := a.tmp()
	a.mov(t2, a.I())
	a.subImm(t2, 8)
	a.ldrReg(a.B(), a.In(), t2)
	a.b.Label(doneS)
}

func (a *arm64Rapid) ldrwReg(dst, base, off GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = uint64(m.Load32(m.R[base] + m.R[off])) },
		"ldr %s, [%s, %s]", a.wname(dst), a.GPRName(base), a.GPRName(off))
}

func (a *arm64Rapid) Short1to3() {
	// a = p[0]<<45 | p[n-1]; b = p[n>>1]
	t := a.tmp()
	a.ldrb(a.A(), a.In(), 0)
	a.lsl(a.A(), a.A(), 45)
	a.mov(t, a.I())
	a.subImm(t, 1)
	a.ldrbReg(a.B(), a.In(), t)
	a.orr(a.A(), a.A(), a.B())
	a.lsr(t, a.I(), 1)
	a.ldrbReg(a.B(), a.In(), t)
}

func (a *arm64Rapid) Tail16() {
	// a = load(in + i - 16) ^ i; b = load(in + i - 8)
	t := a.tmp()
	a.add(t, a.In(), a.I())
	a.ldr(a.A(), t, -16)
	a.ldr(a.B(), t, -8)
	a.eor(a.A(), a.A(), a.I())
}

func (a *arm64Rapid) Finalize() {
	// a ^= secret[1]; b ^= seed; a, b = mum(a, b)
	// ret = mix(a ^ secret[7], b ^ secret[1] ^ i)
	s1 := a.tmp()
	a.ldrSecret(s1, 1)
	a.eor(a.A(), a.A(), s1)
	a.eor(a.B(), a.B(), a.Seed())

	// mum: both halves, and both are needed, so this is not mix.
	lo, hi := a.See(1), a.See(2) // dead by now; any two scratch registers
	a.umulh(hi, a.A(), a.B())
	a.mul(lo, a.A(), a.B())

	s7 := a.A()
	a.ldrSecret(s7, 7)
	a.eor(lo, lo, s7)
	a.eor(hi, hi, s1)
	a.eor(hi, hi, a.I())
	a.mix(a.RetGPR(), lo, hi)
}

func (a *arm64Rapid) AdvanceIn(bytes int) { a.addImm(a.In(), int64(bytes)) }
func (a *arm64Rapid) SubI(bytes int)      { a.subImm(a.I(), int64(bytes)) }
