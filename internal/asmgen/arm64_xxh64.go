package asmgen

// The XXH64 kernel on arm64, with the lane round in two forms that differ
// in one instruction, both in the one kernel and chosen by its last argument.
//
// The round is v = rol(v + x*P2, 31) * P1. The natural arm64 spelling fuses
// the multiply and the add: madd v, x, P2, v; ror; mul -- three instructions,
// and the shape cespare/xxhash ships. But the addend of that madd is the
// previous round's mul, and on Apple's cores a multiply-accumulate forwards
// its addend in one cycle only from another multiply-accumulate; from a plain
// mul it waits the full multiplier latency. Measured on an M2: madd, ror, mul
// is a 7-cycle chain per block, mul, add, ror, mul is 5 -- 20.9 GB/s against
// 15.9. The split form costs one more instruction per lane, which a
// four-wide, issue-bound core (Neoverse N2, where the fused form measures at
// its chain bound already) would pay for. So both loops are generated, and
// the split argument picks one: the fused loop by default, the split loop
// where the core is known to want it. See CLAUDE.md.
//
// Register plan:
//
//	x0 in / lanes   x1 n / in    x2 seed, then block count
//	x4-x8 P1..P5    x9 h         x10-x13 v1..v4               x14-x17 words
//	x19 Tmp         x23 primes table
//
// The primes come from a table in the Go package: five 64-bit immediates would
// be four instructions each in code with no constant pool, where the table is
// three loads.

type arm64Scalar struct {
	*arm64
}

func newARM64Scalar() *arm64Scalar {
	return &arm64Scalar{arm64: &arm64{b: &Builder{}, name: "scalar"}}
}

func (a *arm64Scalar) Dual() bool    { return true }
func (a *arm64Scalar) RetGPR() GPR   { return 9 }
func (a *arm64Scalar) TableGPR() GPR { return 23 }
func (a *arm64Scalar) H() GPR        { return 9 }
func (a *arm64Scalar) V(i int) GPR   { return GPR(10 + i) }
func (a *arm64Scalar) X() GPR        { return 14 }
func (a *arm64Scalar) Tmp() GPR      { return 19 }

// p is the register holding prime n+1: x4..x8, all loaded up front.
func (a *arm64Scalar) p(n int) GPR { return GPR(4 + n) }

func (a *arm64Scalar) LoadPrimes() {
	t := a.TableGPR()
	a.LoadPair(a.p(0), a.p(1), t, 0)
	a.LoadPair(a.p(2), a.p(3), t, 16)
	a.Load64(a.p(4), t, 32)
}

func (a *arm64Scalar) AddPrime(dst GPR, n int) { a.Add(dst, a.p(n)) }
func (a *arm64Scalar) MulPrime(dst GPR, n int) { a.mul3(dst, dst, a.p(n)) }

func (a *arm64Scalar) MulAddPrime(dst GPR, mul, add int) {
	m, ad := a.p(mul), a.p(add)
	a.b.emit(func(mc *Machine) { mc.R[dst] = mc.R[dst]*mc.R[m] + mc.R[ad] },
		"madd %s, %s, %s, %s", a.GPRName(dst), a.GPRName(dst), a.GPRName(m), a.GPRName(ad))
}

// LoadSplit reads the lane-round form from the table's sixth slot. It sits
// after the length branch, so a short hash never executes it.
func (a *arm64Scalar) LoadSplit(dst GPR) { a.Load64(dst, a.TableGPR(), 40) }

func (a *arm64Scalar) Load64(dst, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base] + uint64(off)) },
		"ldr %s, [%s, #%d]", a.GPRName(dst), a.GPRName(base), off)
}

func (a *arm64Scalar) Load32(dst, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load32(m.R[base] + uint64(off)) },
		"ldr w%d, [%s, #%d]", int(dst), a.GPRName(base), off)
}

func (a *arm64Scalar) Load8(dst, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load8(m.R[base] + uint64(off)) },
		"ldrb w%d, [%s, #%d]", int(dst), a.GPRName(base), off)
}

func (a *arm64Scalar) LoadPair(d0, d1, base GPR, off int) { a.ldp(d0, d1, base, off) }

// The post-indexed forms: load, then base += inc, in one instruction.
func (a *arm64Scalar) Load64Adv(dst, base GPR, inc int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[base]); m.R[base] += uint64(inc) },
		"ldr %s, [%s], #%d", a.GPRName(dst), a.GPRName(base), inc)
}

func (a *arm64Scalar) Load32Adv(dst, base GPR, inc int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load32(m.R[base]); m.R[base] += uint64(inc) },
		"ldr w%d, [%s], #%d", int(dst), a.GPRName(base), inc)
}

func (a *arm64Scalar) Load8Adv(dst, base GPR, inc int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load8(m.R[base]); m.R[base] += uint64(inc) },
		"ldrb w%d, [%s], #%d", int(dst), a.GPRName(base), inc)
}

func (a *arm64Scalar) Store64(src, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.Store64(m.R[base]+uint64(off), m.R[src]) },
		"str %s, [%s, #%d]", a.GPRName(src), a.GPRName(base), off)
}

func (a *arm64Scalar) Mov(dst, src GPR)          { a.MovRR(dst, src) }
func (a *arm64Scalar) Add(dst, src GPR)          { a.AddRR(dst, src) }
func (a *arm64Scalar) AddImm(dst GPR, imm int64) { a.AddRI(dst, imm) }
func (a *arm64Scalar) Sub(dst, src GPR)          { a.SubRR(dst, src) }
func (a *arm64Scalar) Shr(dst GPR, sh uint)      { a.ShrRI(dst, sh) }
func (a *arm64Scalar) Xor(dst, src GPR)          { a.eor3(dst, dst, src) }

// Rol is a rotate right by the complement: arm64 has only ror.
func (a *arm64Scalar) Rol(dst GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[dst]<<sh | m.R[dst]>>(64-sh) },
		"ror %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), 64-sh)
}

func (a *arm64Scalar) Rol3(dst, src GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src]<<sh | m.R[src]>>(64-sh) },
		"ror %s, %s, #%d", a.GPRName(dst), a.GPRName(src), 64-sh)
}

// InitLanes with three-operand adds: five instructions.
func (a *arm64Scalar) InitLanes(seed GPR, v [4]GPR) {
	p1, p2 := a.p(0), a.p(1)
	a.add3(v[0], seed, p1)
	a.Add(v[0], p2)
	a.add3(v[1], seed, p2)
	a.Mov(v[2], seed)
	a.b.emit(func(m *Machine) { m.R[v[3]] = m.R[seed] - m.R[p1] },
		"sub %s, %s, %s", a.GPRName(v[3]), a.GPRName(seed), a.GPRName(p1))
}

func (a *arm64Scalar) XorShr(dst GPR, sh uint) { a.eorShr(dst, dst, dst, sh) }

// Round0 is x = rol(x*P2, 31) * P1.
func (a *arm64Scalar) Round0(r GPR) {
	a.MulPrime(r, 1)
	a.Rol(r, 31)
	a.MulPrime(r, 0)
}

// Block pairs the loads and then runs the four rounds, in the form asked
// for.
func (a *arm64Scalar) Block(in GPR, off int, v [4]GPR, split bool) {
	x := [4]GPR{14, 15, 16, 17}
	a.ldp(x[0], x[1], in, off)
	a.ldp(x[2], x[3], in, off+16)
	for i := 0; i < 4; i++ {
		vi, xi, p2 := v[i], x[i], a.p(1)
		if !split {
			// v = v + x*P2, in one instruction whose addend waits on the
			// previous round's mul.
			a.b.emit(func(m *Machine) { m.R[vi] += m.R[xi] * m.R[p2] },
				"madd %s, %s, %s, %s", a.GPRName(vi), a.GPRName(xi), a.GPRName(p2), a.GPRName(vi))
		} else {
			a.MulPrime(xi, 1)
			a.Add(vi, xi)
		}
		a.Rol(vi, 31)
		a.MulPrime(vi, 0)
	}
}

func (a *arm64Scalar) BranchBitClear(r GPR, bit uint, label string) {
	a.b.emit(func(m *Machine) {
		if m.R[r]>>bit&1 == 0 {
			m.jump(label)
		}
	}, "tbz %s, #%d, %s", a.GPRName(r), bit, label)
}

func (a *arm64Scalar) BranchZero(r GPR, label string) {
	a.b.emit(func(m *Machine) {
		if m.R[r] == 0 {
			m.jump(label)
		}
	}, "cbz %s, %s", a.GPRName(r), label)
}

func (a *arm64Scalar) MovPrime(dst GPR, n int) { a.Mov(dst, a.p(n)) }

func (a *arm64Scalar) Neg(dst GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = -m.R[dst] },
		"neg %s, %s", a.GPRName(dst), a.GPRName(dst))
}

func (a *arm64Scalar) ScratchGPR() GPR { return a.ArgGPR(2) }

// UnseededTwin is off here: the twin saves a handful of instructions that an
// arm64 core with three-operand adds mostly does not pay, and no arm64 has
// been measured with it. See the x86 face.
func (a *arm64Scalar) UnseededTwin() bool { return false }

// VendorSplit is off here: the primes reach the kernel through a pointer
// already -- five 64-bit immediates would be four instructions each -- so
// there is no second form to choose between, and no arm64 core has been
// measured wanting one.
func (a *arm64Scalar) VendorSplit() bool { return false }

// UsePointerPrimes is unreachable: this backend has one prime form, and it is
// the pointer one.
func (a *arm64Scalar) UsePointerPrimes() { panic("asmgen: arm64 xxh64 has no second prime form") }

func (a *arm64Scalar) BranchMaskClear(r GPR, mask int64, label string) {
	a.b.emit(func(m *Machine) { m.setCmp(m.R[r]&uint64(mask), 0) },
		"tst %s, #%d", a.GPRName(r), uint64(mask))
	a.branch(EQ, label)
}

// TailSkips is empty here, keeping the kernel byte-identical: the M2 was
// already level with cespare/xxhash at these lengths, and the skips have not
// been measured on an arm64 core. See the x86 face for what they measured
// there, which is nothing to keep.
func (a *arm64Scalar) TailSkips() int { return 0 }
