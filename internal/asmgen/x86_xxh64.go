package asmgen

// The XXH64 kernel on x86-64. There is one form: the lane loop is bound by
// the integer multiplier -- eight imul per 32-byte block at one per cycle on
// every current core, against a five-cycle dependency chain per lane -- and
// nothing in the scalar instruction set changes that. See CLAUDE.md for the
// vector-assisted variant that could, and why it is not here.
//
// Register plan, in thirteen general-purpose registers:
//
//	rdi in / lanes    rsi n / in         rdx seed, then block count
//	rax h             r8-r11 v1..v4      r12 x (also Tmp)
//	r13 P1            rbx P2             r14 P4             rcx P5
//
// Four of the five primes live in registers, loaded RIP-relative by the
// prologue; there is no table pointer. That is the whole difference between
// this kernel and cespare/xxhash's on a 32..256-byte hash. Reaching the
// primes through a pointer instead -- LEA the table, load off the base --
// costs 12-16% there on a Redwood Cove. The measurement was made both ways:
// adding the pointer to a copy of cespare's kernel and taking nothing else
// away moved it from 30.4 to 34.9 cycles at 64 bytes and 46.8 to 55.0 at
// 128, and from this side, removing up to twelve instructions elsewhere -- a
// cheaper tail, a single-block loop, seed-free lanes -- moved not one cycle.
//
// Why it costs that much is not understood, and the obvious explanation is
// wrong: it is not the loads waiting on the LEA, because a *dead* LEA in the
// same place costs the same. See CLAUDE.md before theorizing further, and do
// not carry the conclusion to other kernels -- XXH3 executes one of these per
// hash for free.
//
// R14 is the goroutine pointer only in ABIInternal; see x86.go for why an
// ABI0 leaf may have it.
//
// P3 is the odd one out: thirteen registers is one short of holding all five
// primes alongside the hash's own nine, and P3 is used twice, both times off
// the hot path -- the tail's four-byte step and the second multiply of the
// avalanche. It is materialized there as a ten-byte movabs into the scratch
// register, which is dead at both points. Materializing all five that way
// was tried and lost: five movabs in the prologue is fifty bytes every call
// pays, and on a Zen 3 that put the short hashes 6-19% behind cespare.

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

// TableGPR is -1: the prologue reads the table into registers rather than
// keeping its address. See TableLoads.
func (x *x86Scalar) TableGPR() GPR { return -1 }
func (x *x86Scalar) H() GPR        { return rAX }
func (x *x86Scalar) V(i int) GPR   { return []GPR{r8, r9, r10, r11}[i] }
func (x *x86Scalar) X() GPR        { return r12 }
func (x *x86Scalar) Tmp() GPR      { return r12 }

// x86PrimeReg is where the primes live. P3 is absent: it is materialized as
// an immediate at its two uses, both of which have the scratch register free.
var x86PrimeReg = map[int]GPR{0: r13, 1: rBX, 3: r14, 4: rCX}

// x86PrimeVal is the value of each prime, for the ones emitted as immediates.
// It repeats what xxh64.go holds, which is safe only because nothing checks
// that they agree except the tests that matter: TestSimulatedBackends runs
// this instruction stream against sum64Generic, which reads xxh64.go's
// constants, so a value wrong here fails there.
var x86PrimeVal = [5]uint64{
	0x9E3779B185EBCA87, 0xC2B2AE3D27D4EB4F, 0x165667B19E3779F9,
	0x85EBCA77C2B2AE63, 0x27D4EB2F165667C5,
}

// TableLoads is what the prologue lifts out of the primes table. The
// streaming kernel runs nothing but the lane loop, so it takes only the two
// primes that loop multiplies by.
func (x *x86Scalar) TableLoads(def FuncDef) []TableLoad {
	slots := []int{0, 1}
	if def.Ret != "" { // the whole-hash kernel: merge, tail and avalanche too
		slots = []int{0, 1, 3, 4}
	}
	var out []TableLoad
	for _, s := range slots {
		out = append(out, TableLoad{Slot: s, Reg: x86PrimeReg[s]})
	}
	return out
}

// withPrime hands op the operand naming prime n, materializing it into the
// scratch register first when it has no register of its own. dst must not be
// that scratch register: the movabs would land on top of it.
func (x *x86Scalar) withPrime(dst GPR, n int, op func(operand string, val func(*Machine) uint64)) {
	if r, ok := x86PrimeReg[n]; ok {
		op(x.GPRName(r), func(m *Machine) uint64 { return m.R[r] })
		return
	}
	if dst == x.Tmp() {
		panic("asmgen: x86 xxh64 prime immediate would clobber its own destination")
	}
	v := x86PrimeVal[n]
	t := x.Tmp()
	x.b.emit(func(m *Machine) { m.R[t] = v }, "movabsq $%#x, %s", v, x.GPRName(t))
	op(x.GPRName(t), func(m *Machine) uint64 { return m.R[t] })
}

// x86GPR32 names the low 32 bits of a register, for the loads that zero-extend
// into the full register.
var x86GPR32 = map[GPR]string{
	rAX: "%eax", rCX: "%ecx", rDX: "%edx", rBX: "%ebx",
	rSI: "%esi", rDI: "%edi", r8: "%r8d", r9: "%r9d",
	r10: "%r10d", r11: "%r11d", r12: "%r12d", r13: "%r13d", r14: "%r14d",
}

// LoadPrimes does nothing here: the prologue has already put them in
// registers, which is the point. See TableLoads.
func (x *x86Scalar) LoadPrimes() {}

func (x *x86Scalar) AddPrime(dst GPR, n int) {
	x.withPrime(dst, n, func(op string, val func(*Machine) uint64) {
		x.b.emit(func(m *Machine) { m.R[dst] += val(m) },
			"addq %s, %s", op, x.GPRName(dst))
	})
}

func (x *x86Scalar) MulPrime(dst GPR, n int) {
	x.withPrime(dst, n, func(op string, val func(*Machine) uint64) {
		x.b.emit(func(m *Machine) { m.R[dst] *= val(m) },
			"imulq %s, %s", op, x.GPRName(dst))
	})
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

func (x *x86Scalar) BranchMaskClear(r GPR, mask int64, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[r]&uint64(mask), 0) },
		"testq $%d, %s", uint64(mask), x.GPRName(r))
	x.branch(EQ, label)
}

// TailMaskSkips is on here: five taken branches in the dozen instructions of
// a short hash were most of the gap to cespare/xxhash on a Zen 4.
func (x *x86Scalar) TailMaskSkips() bool { return true }

func (x *x86Scalar) BranchZero(r GPR, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[r], 0) },
		"testq %s, %s", x.GPRName(r), x.GPRName(r))
	x.branch(EQ, label)
}
