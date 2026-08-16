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
//	r13 P1            rbx P2             rcx the primes table
//
// P1 and P2, which every lane round uses, sit in registers; P3, P4 and P5,
// used once per merge round or tail step, are memory operands of the add or
// imul that uses them -- one instruction each, and the load is hot. That is
// also why the primes are not immediates: a 64-bit movabs is a ten-byte
// instruction, five of them are fifty bytes of prologue, and on a Zen 3 the
// sweep across hardware had the short hashes 6-19% behind cespare/xxhash,
// which reads its primes from a table, while level everywhere else.

// x86Scalar is the x86 backend's XXH64 face. It shares the Builder and the
// integer emitters of x86 and adds the scalar ones the hash needs.
type x86Scalar struct {
	*x86
}

func newX86Scalar() *x86Scalar {
	return &x86Scalar{x86: &x86{b: &Builder{}, name: "scalar"}}
}

func (x *x86Scalar) RetGPR() GPR { return rAX }

// LoadSplit is unreachable: this backend has one lane-round form.
func (x *x86Scalar) LoadSplit(GPR) { panic("asmgen: x86 xxh64 is not dual") }
func (x *x86Scalar) TableGPR() GPR { return rCX }
func (x *x86Scalar) H() GPR        { return rAX }
func (x *x86Scalar) V(i int) GPR   { return []GPR{r8, r9, r10, r11}[i] }
func (x *x86Scalar) X() GPR        { return r12 }
func (x *x86Scalar) Tmp() GPR      { return r12 }

// x86PrimeReg is where P1 and P2 live; the other primes are read from the
// table.
var x86PrimeReg = map[int]GPR{0: r13, 1: rBX}

// prime renders prime n as an operand: its register, or its slot in the
// table.
func (x *x86Scalar) prime(n int) string {
	if r, ok := x86PrimeReg[n]; ok {
		return x.GPRName(r)
	}
	return x.mem(rCX, 8*n)
}

// primeVal is the simulator's view of the same.
func (x *x86Scalar) primeVal(m *Machine, n int) uint64 {
	if r, ok := x86PrimeReg[n]; ok {
		return m.R[r]
	}
	return m.Load64(m.R[rCX] + uint64(8*n))
}

// x86GPR32 names the low 32 bits of a register, for the loads that zero-extend
// into the full register.
var x86GPR32 = map[GPR]string{
	rAX: "%eax", rCX: "%ecx", rDX: "%edx", rBX: "%ebx",
	rSI: "%esi", rDI: "%edi", r8: "%r8d", r9: "%r9d",
	r10: "%r10d", r11: "%r11d", r12: "%r12d", r13: "%r13d",
}

func (x *x86Scalar) LoadPrimes() {
	x.Load64(r13, rCX, 0)
	x.Load64(rBX, rCX, 8)
}

func (x *x86Scalar) AddPrime(dst GPR, n int) {
	x.b.emit(func(m *Machine) { m.R[dst] += x.primeVal(m, n) },
		"addq %s, %s", x.prime(n), x.GPRName(dst))
}

func (x *x86Scalar) MulPrime(dst GPR, n int) {
	x.b.emit(func(m *Machine) { m.R[dst] *= x.primeVal(m, n) },
		"imulq %s, %s", x.prime(n), x.GPRName(dst))
}

func (x *x86Scalar) MulAddPrime(dst GPR, mul, add int) {
	x.MulPrime(dst, mul)
	x.AddPrime(dst, add)
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
	x.AddPrime(v[0], 0)
	x.AddPrime(v[0], 1)
	x.Mov(v[1], seed)
	x.AddPrime(v[1], 1)
	x.Mov(v[2], seed)
	x.Mov(v[3], seed)
	x.Sub(v[3], r13)
}

func (x *x86Scalar) XorShr(dst GPR, sh uint) {
	t := x.Tmp()
	x.Mov(t, dst)
	x.Shr(t, sh)
	x.Xor(dst, t)
}

// Round0 is x = rol(x*P2, 31) * P1.
func (x *x86Scalar) Round0(r GPR) {
	x.MulPrime(r, 1)
	x.Rol(r, 31)
	x.MulPrime(r, 0)
}

func (x *x86Scalar) Dual() bool { return false }

// Block loads each word into the one scratch register and absorbs it before
// the next: renaming makes the reuse free, and there is no fifth register.
// There is one round shape here, so split is ignored.
func (x *x86Scalar) Block(in GPR, off int, v [4]GPR, split bool) {
	w := x.X()
	for i := 0; i < 4; i++ {
		x.Load64(w, in, off+8*i)
		x.MulPrime(w, 1)
		x.Add(v[i], w)
		x.Rol(v[i], 31)
		x.MulPrime(v[i], 0)
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
