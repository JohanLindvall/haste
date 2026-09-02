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
// Two forms of that plan, chosen by CPUID vendor; see VendorSplit. In the
// register form, which Intel takes, four of the five primes live in
// registers loaded RIP-relative by the prologue and there is no table
// pointer; in the pointer form, which everything else takes, rCX holds the
// table and P3, P4 and P5 are memory operands off it. On a Redwood Cove the
// register form is worth 12-16% over 32..256 bytes and on a Zen 3 it costs
// 5-17% over 8..256. Neither is understood.
//
// What is known about the Intel half: adding a pointer to a copy of
// cespare/xxhash's kernel and taking nothing else away moved it from 30.4 to
// 34.9 cycles at 64 bytes and 46.8 to 55.0 at 128, while removing up to
// twelve instructions elsewhere -- a cheaper tail, a single-block loop,
// seed-free lanes -- moved not one cycle. The obvious explanation is wrong:
// it is not the loads waiting on the LEA, because a LEA that is executed and
// never used costs the same, while one that is branched over costs nothing.
// Do not carry the conclusion to other kernels: XXH3 executes one of these
// per hash for free.
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
	// ptrPrimes selects the second form: the primes are reached through a
	// pointer to the table rather than held in registers. See VendorSplit.
	ptrPrimes bool
}

func newX86Scalar() *x86Scalar {
	return &x86Scalar{x86: &x86{b: &Builder{}, name: "scalar"}}
}

// VendorSplit is on: Intel and AMD want opposite things from this kernel.
// Holding the primes in registers is worth 12-16% over 32..256 bytes on a
// Redwood Cove and costs 5-17% over 8..256 on a Zen 3, so both forms are
// emitted and dispatch_amd64.go picks by CPUID vendor.
func (x *x86Scalar) VendorSplit() bool { return true }

// UsePointerPrimes switches this backend to the pointer form. It must be
// called before anything is emitted.
func (x *x86Scalar) UsePointerPrimes() { x.ptrPrimes = true }

func (x *x86Scalar) RetGPR() GPR { return rAX }

// LoadSplit is unreachable: this backend has one lane-round form.
func (x *x86Scalar) LoadSplit(GPR) { panic("asmgen: x86 xxh64 is not dual") }

// TableGPR is the table pointer in the pointer form and -1 in the register
// form, where the prologue reads the constants out instead. See TableLoads.
func (x *x86Scalar) TableGPR() GPR {
	if x.ptrPrimes {
		return rCX
	}
	return -1
}
func (x *x86Scalar) H() GPR      { return rAX }
func (x *x86Scalar) V(i int) GPR { return []GPR{r8, r9, r10, r11}[i] }
func (x *x86Scalar) X() GPR      { return r12 }
func (x *x86Scalar) Tmp() GPR    { return r12 }

// x86PrimeReg is where the primes live in the register form. P3 is absent: it
// is materialized as an immediate at its two uses, both of which have the
// scratch register free.
var x86PrimeReg = map[int]GPR{0: r13, 1: rBX, 3: r14, 4: rCX}

// x86PrimeRegPtr is where they live in the pointer form: only the two the
// lane loop multiplies by, the rest being memory operands off rCX, which
// holds the table. That is one register fewer than the hash has to spare,
// which is why this form needs no immediate and no R14.
var x86PrimeRegPtr = map[int]GPR{0: r13, 1: rBX}

// primeReg is the map this backend's form uses.
func (x *x86Scalar) primeReg() map[int]GPR {
	if x.ptrPrimes {
		return x86PrimeRegPtr
	}
	return x86PrimeReg
}

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
	if x.ptrPrimes {
		// The pointer form takes the address instead; TableGPR says so, and
		// LoadPrimes reads P1 and P2 off it in the body.
		return nil
	}
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
	if r, ok := x.primeReg()[n]; ok {
		op(x.GPRName(r), func(m *Machine) uint64 { return m.R[r] })
		return
	}
	if x.ptrPrimes {
		// Off the table pointer, one instruction and an L1-hot load.
		off := 8 * n
		op(x.mem(rCX, off), func(m *Machine) uint64 { return m.Load64(m.R[rCX] + uint64(off)) })
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

// LoadPrimes does nothing in the register form: the prologue has already put
// them there, which is the point. The pointer form reads the two the lane
// loop needs off the table.
func (x *x86Scalar) LoadPrimes() {
	if !x.ptrPrimes {
		return
	}
	x.Load64(r13, rCX, 0)
	x.Load64(rBX, rCX, 8)
}

func (x *x86Scalar) AddPrime(dst GPR, n int) {
	x.withPrime(dst, n, func(op string, val func(*Machine) uint64) {
		x.b.emit(func(m *Machine) { m.R[dst] += val(m) },
			"addq %s, %s", op, x.GPRName(dst))
	})
}

func (x *x86Scalar) MovPrime(dst GPR, n int) {
	x.withPrime(dst, n, func(op string, val func(*Machine) uint64) {
		x.b.emit(func(m *Machine) { m.R[dst] = val(m) },
			"movq %s, %s", op, x.GPRName(dst))
	})
}

func (x *x86Scalar) Neg(dst GPR) {
	x.b.emit(func(m *Machine) { m.R[dst] = -m.R[dst] }, "negq %s", x.GPRName(dst))
}

// ScratchGPR is the seeded form's third argument register, free in the twin.
func (x *x86Scalar) ScratchGPR() GPR { return rDX }

// UnseededTwin: measured on a Zen 4, every length 1..40, four relinked
// layouts -- the twin is worth 2.5 points against cespare/xxhash over 1..8
// bytes and 0.7 over 1..40.
func (x *x86Scalar) UnseededTwin() bool { return true }

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

// TailSkips is empty. The skips were on while the prologue materialized the
// five primes with movabs: fifty bytes of ten-byte instructions, against
// which skipping three or four taken branches in the tail was worth 12-17%
// of a 4- or 8-byte hash. Holding the primes in registers removed that
// prologue, and with it the thing the skips were paying for -- what is left
// is their cost, a not-taken test on every length whose tail runs the steps
// the skip would have jumped over. Measured on a Zen 4, four relinked
// layouts each, every length 1..40: off beats all four skips by 4.8 points
// over 9..16 bytes and 3.2 over 17..32.
//
// Re-measured in 2026-09 the other way round, with every subset of interest
// as its own symbol in one binary, each kernel 64-byte aligned and one
// benchmark loop calling all of them, so that neither the caller's nor the
// kernel's placement differs between them: against no skips, the bytes skip
// alone reads +2.8% over 1..40, the words skip alone +2.6%, the two together
// +2.2% and all four +4.1%, with only 4 bytes (-7%) and 32 (-2%) gaining
// from any of them. The counters had put the 4- and 8-byte deficit against
// cespare/xxhash on two extra taken branches and a front end starved 23% of
// its slots against 8%; removing the branches did not return the cycles, so
// that is not where they go. X86TailSkips is the set the kernel takes, and
// the generator's -tailskips flag is how a subset is tried.
func (x *x86Scalar) TailSkips() int { return X86TailSkips }

// X86TailSkips is the skip set the x86 kernel takes; see TailSkips.
var X86TailSkips = 0

func (x *x86Scalar) BranchZero(r GPR, label string) {
	x.b.emit(func(m *Machine) { m.setCmp(m.R[r], 0) },
		"testq %s, %s", x.GPRName(r), x.GPRName(r))
	x.branch(EQ, label)
}
