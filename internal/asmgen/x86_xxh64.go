package asmgen

// The XXH64 kernel on x86-64. There is one form: the lane loop is bound by
// the integer multiplier -- eight imul per 32-byte block at one per cycle on
// every current core, against a five-cycle dependency chain per lane -- and
// nothing in the scalar instruction set changes that. See CLAUDE.md for the
// vector-assisted variant that could, and why it is not here.
//
// Register plan, in twelve general-purpose registers:
//
//	rdi in / lanes    rsi n / in         rdx seed, then block count
//	rax h             r8-r11 v1..v4      r12 x (also Tmp)
//	r13 P1            rbx P2             rcx P4
//	r8 P3, r9 P5 once the lanes are dead
//
// P3 and P5 are needed only in the tail, after the merge has consumed the
// lanes, which is what makes the plan fit; LoadTailPrimes is where they
// arrive. All primes are immediates: movabs is one instruction here.

// x86Scalar is the x86 backend's XXH64 face. It shares the Builder and the
// integer emitters of x86 and adds the scalar ones the hash needs.
type x86Scalar struct {
	*x86
}

func newX86Scalar() *x86Scalar {
	return &x86Scalar{x86: &x86{b: &Builder{}, name: "scalar"}}
}

const (
	xxh64Prime1 uint64 = 0x9E3779B185EBCA87
	xxh64Prime2 uint64 = 0xC2B2AE3D27D4EB4F
	xxh64Prime3 uint64 = 0x165667B19E3779F9
	xxh64Prime4 uint64 = 0x85EBCA77C2B2AE63
	xxh64Prime5 uint64 = 0x27D4EB2F165667C5
)

func (x *x86Scalar) RetGPR() GPR   { return rAX }
func (x *x86Scalar) TableGPR() GPR { return -1 }
func (x *x86Scalar) H() GPR        { return rAX }
func (x *x86Scalar) V(i int) GPR   { return []GPR{r8, r9, r10, r11}[i] }
func (x *x86Scalar) X() GPR        { return r12 }
func (x *x86Scalar) Tmp() GPR      { return r12 }
func (x *x86Scalar) P(i int) GPR   { return []GPR{r13, rBX, r8, rCX, r9}[i] }

// x86GPR32 names the low 32 bits of a register, for the loads that zero-extend
// into the full register.
var x86GPR32 = map[GPR]string{
	rAX: "%eax", rCX: "%ecx", rDX: "%edx", rBX: "%ebx",
	rSI: "%esi", rDI: "%edi", r8: "%r8d", r9: "%r9d",
	r10: "%r10d", r11: "%r11d", r12: "%r12d", r13: "%r13d",
}

func (x *x86Scalar) movabs(dst GPR, imm uint64) {
	x.b.emit(func(m *Machine) { m.R[dst] = imm },
		"movabsq $%d, %s", imm, x.GPRName(dst))
}

func (x *x86Scalar) LoadPrimes() {
	x.movabs(x.P(0), xxh64Prime1)
	x.movabs(x.P(1), xxh64Prime2)
	x.movabs(x.P(3), xxh64Prime4)
}

func (x *x86Scalar) LoadTailPrimes() {
	x.movabs(x.P(2), xxh64Prime3)
	x.movabs(x.P(4), xxh64Prime5)
}

func (x *x86Scalar) Load64(dst, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + uint64(off)) },
		"movq %s, %s", x.mem(base, off), x.GPRName(dst))
}

func (x *x86Scalar) Load32(dst, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load32(m.R[base] + uint64(off)) },
		"movl %s, %s", x.mem(base, off), x86GPR32[dst])
}

func (x *x86Scalar) Load8(dst, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.Load8(m.R[base] + uint64(off)) },
		"movzbl %s, %s", x.mem(base, off), x86GPR32[dst])
}

func (x *x86Scalar) LoadPair(d0, d1, base GPR, off int) {
	x.Load64(d0, base, off)
	x.Load64(d1, base, off+8)
}

// x86 has no post-indexed load: the advance is its own instruction.
func (x *x86Scalar) Load64Adv(dst, base GPR, inc int) {
	x.Load64(dst, base, 0)
	x.AddRI(base, int64(inc))
}
func (x *x86Scalar) Load32Adv(dst, base GPR, inc int) {
	x.Load32(dst, base, 0)
	x.AddRI(base, int64(inc))
}
func (x *x86Scalar) Load8Adv(dst, base GPR, inc int) {
	x.Load8(dst, base, 0)
	x.AddRI(base, int64(inc))
}

func (x *x86Scalar) Store64(src, base GPR, off int) {
	x.b.emit(func(m *Machine) { m.Store64(m.R[base]+uint64(off), m.R[src]) },
		"movq %s, %s", x.GPRName(src), x.mem(base, off))
}

func (x *x86Scalar) Mov(dst, src GPR)          { x.MovRR(dst, src) }
func (x *x86Scalar) Add(dst, src GPR)          { x.AddRR(dst, src) }
func (x *x86Scalar) AddImm(dst GPR, imm int64) { x.AddRI(dst, imm) }
func (x *x86Scalar) Sub(dst, src GPR)          { x.SubRR(dst, src) }
func (x *x86Scalar) Shr(dst GPR, sh uint)      { x.ShrRI(dst, sh) }

func (x *x86Scalar) Xor(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] ^= m.R[src] },
		"xorq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86Scalar) Mul(dst, src GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] *= m.R[src] },
		"imulq %s, %s", x.GPRName(src), x.GPRName(dst))
}

func (x *x86Scalar) MulAdd(dst, mul, add GPR) {
	x.Mul(dst, mul)
	x.Add(dst, add)
}

func (x *x86Scalar) Rol(dst GPR, sh uint) {
	x.b.emit(func(m *Machine) { m.R[dst] = m.R[dst]<<sh | m.R[dst]>>(64-sh) },
		"rolq $%d, %s", sh, x.GPRName(dst))
}

func (x *x86Scalar) Rol3(dst, src GPR, sh uint) {
	x.Mov(dst, src)
	x.Rol(dst, sh)
}

// InitLanes in two-operand form: a move and an add per lane that needs one.
func (x *x86Scalar) InitLanes(seed GPR, v [4]GPR) {
	x.Mov(v[0], seed)
	x.Add(v[0], x.P(0))
	x.Add(v[0], x.P(1))
	x.Mov(v[1], seed)
	x.Add(v[1], x.P(1))
	x.Mov(v[2], seed)
	x.Mov(v[3], seed)
	x.Sub(v[3], x.P(0))
}

func (x *x86Scalar) XorShr(dst GPR, sh uint) {
	t := x.Tmp()
	x.Mov(t, dst)
	x.Shr(t, sh)
	x.Xor(dst, t)
}

// Round0 is x = rol(x*P2, 31) * P1.
func (x *x86Scalar) Round0(r GPR) {
	x.Mul(r, x.P(1))
	x.Rol(r, 31)
	x.Mul(r, x.P(0))
}

func (x *x86Scalar) Dual() bool { return false }

// Block loads each word into the one scratch register and absorbs it before
// the next: renaming makes the reuse free, and there is no fifth register.
// There is one round shape here, so split is ignored.
func (x *x86Scalar) Block(in GPR, off int, v [4]GPR, split bool) {
	w := x.X()
	for i := 0; i < 4; i++ {
		x.Load64(w, in, off+8*i)
		x.Mul(w, x.P(1))
		x.Add(v[i], w)
		x.Rol(v[i], 31)
		x.Mul(v[i], x.P(0))
	}
}

func (x *x86Scalar) BranchBitClear(r GPR, bit uint, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[r]&(1<<bit), 0) },
		"testq $%d, %s", uint64(1)<<bit, x.GPRName(r))
	x.branch(EQ, label)
}

func (x *x86Scalar) BranchZero(r GPR, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[r], 0) },
		"testq %s, %s", x.GPRName(r), x.GPRName(r))
	x.branch(EQ, label)
}
