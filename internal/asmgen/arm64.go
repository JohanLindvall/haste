package asmgen

import "fmt"

// arm64 has two backends. NEON is unconditional: every arm64 CPU has it, so it
// is the baseline rather than an upgrade. SVE2 is generated once per vector
// length, because a stripe is a fixed 64 bytes and how many registers that
// occupies is exactly what SVE leaves unspecified; the dispatcher reads the
// hardware's length and picks the matching kernel.

// Go's arm64 ABI reserves R18 (platform), R27 (assembler temporary), R28
// (goroutine), R29 (frame pointer) and R30 (link register); none appear here.
// The seventh and eighth argument registers are x26 and x11: only
// accumBlocks2 has eight arguments, and it has no table for x26 to point at
// and uses five temporaries, so the sixth, x11, is free. Every other register
// below x26 is spoken for in the split kernels. noOverlap in kernel.go holds
// every emitter to its share.
var armArgGPR = []GPR{0, 1, 2, 3, 4, 5, 26, 11}
var armTmpGPR = []GPR{6, 7, 8, 9, 10, 11}

// armConstGPR carries the scramble constant into a vector register.
const armConstGPR GPR = 12

type arm64 struct {
	b      *Builder
	name   string
	unroll int

	sve bool
	vl  int // SVE vector length in bytes; 16 for NEON

	lanes int // 64-bit lanes per vector register
	nvec  int // vector registers per 64-byte stripe

	// scalarLanes is how many of the stripe's eight lanes are absorbed in
	// general-purpose registers rather than vector ones. On a core with two
	// vector pipes the vector units are the bottleneck and the integer units
	// are idle, so moving part of the stripe across shortens the critical
	// resource; on a core with four they are not, and it does not.
	scalarLanes int

	accA, accB []VReg
	// scc holds the scalar lanes' accumulators. There is no second set: the
	// deferred lane swap exists to avoid a vector shuffle, and in registers
	// the neighbouring lane is just a different register name, so those lanes
	// use the algorithm's own form and are always materialized.
	scc []GPR
	// ssec holds the scalar lanes' secret words across an unrolled group.
	// The secret advances one 64-bit word per stripe, so all but one of them
	// are already in registers; only the word entering the window is loaded.
	ssec []GPR
	// laneReassoc selects the scalar lane form whose accumulator chain is
	// one add per stripe; see laneMixReassoc.
	laneReassoc bool
	tmp         []VReg
	stmp        []GPR
	kprime      VReg
	// sec8, when nonzero, is a register the unrolled group keeps at the
	// secret pointer plus eight: the odd stripes' base, so that their vector
	// secret loads, which fall on 8 mod 16, can pair up in ldp -- whose
	// immediate must be a multiple of 16 -- rather than go one register at a
	// time. It costs one add per group and saves one load instruction per
	// register pair per odd stripe. Only kernels with a register to spare
	// set it.
	sec8 GPR
	// kprimeHi is kprime shifted into the high half of each 64-bit lane; only
	// the NEON scramble uses it.
	kprimeHi VReg
	// winE and winO, when set, hold the vector lanes' secret across an
	// unrolled group, the way ssec holds the scalar lanes'; see keyWindow.
	winE, winO []VReg
}

// newNEON builds the NEON kernel, which is the arm64 baseline.
//
// Measured on a two-vector-pipe core (Neoverse N2), this loop runs at about
// 10.9 cycles per 64-byte stripe against a marginal rate of ~1.9 vector
// operations per cycle. Moving half the stripe onto the idle integer pipes
// measured out at roughly 7 cycles there, but it costs instruction bandwidth,
// which makes it a regression on four-pipe cores: on an Apple M2 this kernel
// runs at exactly its sixteen vector operations over four pipes, 4.0 cycles
// per stripe, and the four-lane split is 10% behind it. So the kernel stays
// purely vector.
func newNEON(unroll int) *arm64 {
	a := newARM("neon", false, 16, unroll)
	a.sec8 = 13
	return a
}

// newNEONHybrid builds a split kernel: scalarLanes of the stripe's eight
// lanes go through general-purpose registers, the rest through NEON.
//
// The four-lane split runs eight stripes an iteration with its vector keys
// from a window (see stripeNEONWindow): the loop is bound by the operations
// the N2 dispatches, and the unroll halves what its bookkeeping costs a
// stripe.
//
// Four is the N2's split, by measurement rather than structure: two leaves
// thirteen vector operations per stripe, and that core sustains about 1.5
// of those per cycle, which costs more than the instructions it saves. Two
// is the other shape a four-vector-pipe core with a wide front end can use,
// where the four-lane split loses on the integer side (its multiply-
// accumulates issue at one per cycle on an Apple M2) and pure NEON is bound
// by the vector pipes; it holds the odd vector register in the reference's
// xtn/shrn form. See CLAUDE.md for what each measured.

func newNEONHybrid(name string, unroll, scalarLanes int) *arm64 {
	if scalarLanes%2 != 0 || scalarLanes < 2 || scalarLanes > 4 || unroll%scalarLanes != 0 {
		panic("asmgen: scalar lanes must be 2 or 4 and divide the unroll")
	}
	a := newARM(name, false, 16, unroll)
	a.scalarLanes = scalarLanes
	a.nvec = (accNB - a.scalarLanes) / a.lanes
	a.accA, a.accB = a.accA[:a.nvec], a.accB[:a.nvec]
	// Everything the kernel skeleton does not touch: it holds arguments in
	// x0-x5, its own temporaries in x6-x11 and the scramble constant in x12.
	a.scc = []GPR{17, 19, 20, 21}[:scalarLanes]
	a.ssec = []GPR{22, 23, 24, 25}[:scalarLanes]
	a.stmp = []GPR{13, 14, 15, 16}
	if scalarLanes == 2 {
		// The registers the other two lanes would have held.
		a.stmp = append(a.stmp, 20, 21)
		a.laneReassoc = true
		a.sec8 = 24
	} else {
		// The accumulators take v0-v1 and v4-v5 here, leaving v2-v3 and
		// v6-v7 for the key window.
		a.winE, a.winO = []VReg{2, 3}, []VReg{6, 7}
	}
	return a
}

func newSVE2(vl, unroll int) *arm64 {
	return newARM(fmt.Sprintf("sve2vl%d", vl*8), true, vl, unroll)
}

func newARM(name string, sve bool, vl, unroll int) *arm64 {
	a := &arm64{b: &Builder{}, name: name, sve: sve, vl: vl, unroll: unroll}
	a.lanes = vl / 8
	a.nvec = accNB / a.lanes
	for i := 0; i < a.nvec; i++ {
		a.accA = append(a.accA, VReg(i))
		a.accB = append(a.accB, VReg(4+i))
	}
	for i := 16; i < 32; i++ {
		a.tmp = append(a.tmp, VReg(i))
	}
	a.kprime = VReg(15)
	a.kprimeHi = VReg(14)
	return a
}

func (a *arm64) Name() string     { return a.name }
func (a *arm64) GOARCH() string   { return "arm64" }
func (a *arm64) Build() *Builder  { return a.b }
func (a *arm64) Unroll() int      { return a.unroll }
func (a *arm64) SecretImm() bool  { return !a.sve }
func (a *arm64) ArgGPR(i int) GPR { return armArgGPR[i] }

// The vector kernels return nothing. The one table any of them reads is
// initAcc, in hashLong, and x26 is the highest register no kernel here
// touches: the split kernels reach x25, and x27 and up are the assembler's
// and the runtime's.
func (a *arm64) RetGPR() GPR      { return 0 }
func (a *arm64) TableGPR() GPR    { return 26 }
func (a *arm64) TmpGPR(i int) GPR { return armTmpGPR[i] }

func (a *arm64) GPRName(r GPR) string { return fmt.Sprintf("x%d", int(r)) }

// q, v and z are the three views of a vector register this backend uses: the
// 128-bit load/store name, the NEON name with an element suffix, and the SVE
// name with an element suffix. They alias, which is why the SVE kernel can
// still reach for a NEON instruction where SVE has nothing better.
func (a *arm64) q(v VReg) string             { return fmt.Sprintf("q%d", int(v)) }
func (a *arm64) v(r VReg, suf string) string { return fmt.Sprintf("v%d.%s", int(r), suf) }
func (a *arm64) z(r VReg, suf string) string { return fmt.Sprintf("z%d.%s", int(r), suf) }

// ---------------------------------------------------------------------------
// Integer unit
// ---------------------------------------------------------------------------

func (a *arm64) MovRR(dst, src GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] },
		"mov %s, %s", a.GPRName(dst), a.GPRName(src))
}

func (a *arm64) MovRI(dst GPR, imm int64) {
	if imm < 0 || imm > 0xffff {
		panic("asmgen: arm64 MovRI immediate out of range")
	}
	a.b.emit(func(m *Machine) { m.R[dst] = uint64(imm) },
		"mov %s, #%d", a.GPRName(dst), imm)
}

func (a *arm64) AddRR(dst, src GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] += m.R[src] },
		"add %s, %s, %s", a.GPRName(dst), a.GPRName(dst), a.GPRName(src))
}

func (a *arm64) AddRI(dst GPR, imm int64) {
	a.b.emit(func(m *Machine) { m.R[dst] += uint64(imm) },
		"add %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), imm)
}

func (a *arm64) SubRR(dst, src GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] -= m.R[src] },
		"sub %s, %s, %s", a.GPRName(dst), a.GPRName(dst), a.GPRName(src))
}

func (a *arm64) SubRI(dst GPR, imm int64) {
	a.b.emit(func(m *Machine) { m.R[dst] -= uint64(imm) },
		"sub %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), imm)
}

func (a *arm64) ShrRI(dst GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] >>= sh },
		"lsr %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), sh)
}

func (a *arm64) ShlRI(dst GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] <<= sh },
		"lsl %s, %s, #%d", a.GPRName(dst), a.GPRName(dst), sh)
}

func (a *arm64) AddRRR(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] + m.R[y] },
		"add %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64) SubRRR(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] - m.R[y] },
		"sub %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64) SubRRI(dst, src GPR, imm int64) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] - uint64(imm) },
		"sub %s, %s, #%d", a.GPRName(dst), a.GPRName(src), imm)
}

func (a *arm64) ShrRRI(dst, src GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] >> sh },
		"lsr %s, %s, #%d", a.GPRName(dst), a.GPRName(src), sh)
}

func (a *arm64) AddShl(dst, x, y GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] + m.R[y]<<sh },
		"add %s, %s, %s, lsl #%d", a.GPRName(dst), a.GPRName(x), a.GPRName(y), sh)
}

// Min is a compare and a conditional select, signed like every count the
// kernels compare.
func (a *arm64) Min(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.setCmp(m.R[x], m.R[y]) },
		"cmp %s, %s", a.GPRName(x), a.GPRName(y))
	a.b.emit(func(m *Machine) {
		if LE.eval(m.cmpA, m.cmpB) {
			m.R[dst] = m.R[x]
		} else {
			m.R[dst] = m.R[y]
		}
	}, "csel %s, %s, %s, le", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

var armCC = map[Cond]string{LT: "lt", GE: "ge", EQ: "eq", NE: "ne", GT: "gt", LE: "le"}

func (a *arm64) BranchI(r GPR, imm int64, c Cond, label string) {
	a.b.emit(func(m *Machine) { m.setCmp(m.R[r], uint64(imm)) },
		"cmp %s, #%d", a.GPRName(r), imm)
	a.branch(c, label)
}

func (a *arm64) SubBranch(r GPR, imm int64, c Cond, label string) {
	a.b.emit(func(m *Machine) {
		m.setCmp(m.R[r], uint64(imm))
		m.R[r] -= uint64(imm)
	}, "subs %s, %s, #%d", a.GPRName(r), a.GPRName(r), imm)
	a.branch(c, label)
}

func (a *arm64) BranchR(x, y GPR, c Cond, label string) {
	a.b.emit(func(m *Machine) { m.setCmp(m.R[x], m.R[y]) },
		"cmp %s, %s", a.GPRName(x), a.GPRName(y))
	a.branch(c, label)
}

func (a *arm64) branch(c Cond, label string) {
	a.b.emit(func(m *Machine) {
		if c.eval(m.cmpA, m.cmpB) {
			m.jump(label)
		}
	}, "b.%s %s", armCC[c], label)
}

func (a *arm64) Jmp(label string) {
	a.b.emit(func(m *Machine) { m.jump(label) }, "b %s", label)
}

// ---------------------------------------------------------------------------
// Vector unit
// ---------------------------------------------------------------------------

// vload reads one vector register from [r+off]. NEON uses the unscaled form,
// whose reach covers every offset the unrolled loop produces; SVE addresses in
// units of the vector length, so off must be a multiple of it.
func (a *arm64) vload(dst VReg, r GPR, off int) {
	sim := func(m *Machine) { m.V[dst] = m.LoadVec(m.R[r]+uint64(off), a.lanes) }
	if a.sve {
		a.b.emit(sim, "ld1d {%s}, p0/z, [%s%s]", a.z(dst, "d"), a.GPRName(r), a.mulVL(off))
		return
	}
	// LDUR reaches -256..255; beyond that the scaled form takes over, which
	// needs the offset to be a multiple of the access size.
	if off >= 256 && off%16 == 0 {
		a.b.emit(sim, "ldr %s, [%s, #%d]", a.q(dst), a.GPRName(r), off)
		return
	}
	a.b.emit(sim, "ldur %s, [%s, #%d]", a.q(dst), a.GPRName(r), off)
}

// vloadPair loads two adjacent vector registers. LDP moves 32 bytes in one
// instruction, which matters once a kernel is limited by how many
// instructions the core can issue rather than by its vector pipes; it needs a
// 16-byte-aligned offset, which the input always has and the secret, stepping
// 8 bytes per stripe, has only half the time.
func (a *arm64) vloadPair(d0, d1 VReg, r GPR, off int) {
	if a.sve || off%16 != 0 || off < -1024 || off > 1008 || int(d1) != int(d0)+1 {
		a.vload(d0, r, off)
		a.vload(d1, r, off+a.vl)
		return
	}
	a.b.emit(func(m *Machine) {
		m.V[d0] = m.LoadVec(m.R[r]+uint64(off), a.lanes)
		m.V[d1] = m.LoadVec(m.R[r]+uint64(off)+16, a.lanes)
	}, "ldp %s, %s, [%s, #%d]", a.q(d0), a.q(d1), a.GPRName(r), off)
}

func (a *arm64) vstore(src VReg, r GPR, off int) {
	sim := func(m *Machine) { m.StoreVec(m.R[r]+uint64(off), m.V[src], a.lanes) }
	if a.sve {
		a.b.emit(sim, "st1d {%s}, p0, [%s%s]", a.z(src, "d"), a.GPRName(r), a.mulVL(off))
		return
	}
	a.b.emit(sim, "stur %s, [%s, #%d]", a.q(src), a.GPRName(r), off)
}

// mulVL renders an SVE offset, which is counted in vector lengths and limited
// to the range -8..7.
func (a *arm64) mulVL(off int) string {
	if off == 0 {
		return ""
	}
	if off%a.vl != 0 {
		panic(fmt.Sprintf("asmgen: SVE offset %d is not a multiple of VL %d", off, a.vl))
	}
	n := off / a.vl
	if n < -8 || n > 7 {
		panic(fmt.Sprintf("asmgen: SVE offset %d out of MUL VL range", off))
	}
	return fmt.Sprintf(", #%d, mul vl", n)
}

func (a *arm64) vxor(dst, x, y VReg) {
	sim := func(m *Machine) {
		for i := 0; i < a.lanes; i++ {
			m.V[dst][i] = m.V[x][i] ^ m.V[y][i]
		}
	}
	if a.sve {
		a.b.emit(sim, "eor %s, %s, %s", a.z(dst, "d"), a.z(x, "d"), a.z(y, "d"))
		return
	}
	a.b.emit(sim, "eor %s, %s, %s", a.v(dst, "16b"), a.v(x, "16b"), a.v(y, "16b"))
}

func (a *arm64) vadd(dst, x, y VReg) {
	sim := func(m *Machine) {
		for i := 0; i < a.lanes; i++ {
			m.V[dst][i] = m.V[x][i] + m.V[y][i]
		}
	}
	if a.sve {
		a.b.emit(sim, "add %s, %s, %s", a.z(dst, "d"), a.z(x, "d"), a.z(y, "d"))
		return
	}
	a.b.emit(sim, "add %s, %s, %s", a.v(dst, "2d"), a.v(x, "2d"), a.v(y, "2d"))
}

func (a *arm64) vshr(dst, src VReg, sh uint) {
	sim := func(m *Machine) {
		for i := 0; i < a.lanes; i++ {
			m.V[dst][i] = m.V[src][i] >> sh
		}
	}
	if a.sve {
		a.b.emit(sim, "lsr %s, %s, #%d", a.z(dst, "d"), a.z(src, "d"), sh)
		return
	}
	a.b.emit(sim, "ushr %s, %s, #%d", a.v(dst, "2d"), a.v(src, "2d"), sh)
}

func (a *arm64) vzero(dst VReg) {
	sim := func(m *Machine) { m.V[dst] = [8]uint64{} }
	if a.sve {
		a.b.emit(sim, "mov %s, #0", a.z(dst, "d"))
		return
	}
	a.b.emit(sim, "movi %s, #0", a.v(dst, "2d"))
}

// uzp1/uzp2 deinterleave the 32-bit halves of two NEON registers at once,
// which is what lets a stripe cost four vector operations per register rather
// than the reference's six.
func (a *arm64) uzp(dst, x, y VReg, odd bool) {
	n := 1
	if odd {
		n = 2
	}
	sim := func(m *Machine) {
		var out [8]uint64
		lo := []uint32{}
		for _, r := range []VReg{x, y} {
			for i := 0; i < a.lanes; i++ {
				if odd {
					lo = append(lo, uint32(m.V[r][i]>>32))
				} else {
					lo = append(lo, uint32(m.V[r][i]))
				}
			}
		}
		for i := 0; i < a.lanes; i++ {
			out[i] = uint64(lo[2*i]) | uint64(lo[2*i+1])<<32
		}
		m.V[dst] = out
	}
	a.b.emit(sim, "uzp%d %s, %s, %s", n, a.v(dst, "4s"), a.v(x, "4s"), a.v(y, "4s"))
}

// umlalNEON accumulates acc += lo*hi over one half of the paired registers.
func (a *arm64) umlalNEON(acc, lo, hi VReg, upper bool) {
	mn, suf := "umlal", "2s"
	if upper {
		mn, suf = "umlal2", "4s"
	}
	a.b.emit(func(m *Machine) {
		for i := 0; i < 2; i++ {
			j := i
			if upper {
				j = i + 2
			}
			l := uint32(m.V[lo][j/2] >> (32 * (j % 2)))
			h := uint32(m.V[hi][j/2] >> (32 * (j % 2)))
			m.V[acc][i] += uint64(l) * uint64(h)
		}
	}, "%s %s, %s, %s", mn, a.v(acc, "2d"), a.v(lo, suf), a.v(hi, suf))
}

// umlalbSVE accumulates acc += even32(x) * even32(y) per 64-bit lane.
func (a *arm64) umlalbSVE(acc, x, y VReg) {
	a.b.emit(func(m *Machine) {
		for i := 0; i < a.lanes; i++ {
			m.V[acc][i] += uint64(uint32(m.V[x][i])) * uint64(uint32(m.V[y][i]))
		}
	}, "umlalb %s, %s, %s", a.z(acc, "d"), a.z(x, "s"), a.z(y, "s"))
}

// ---------------------------------------------------------------------------
// Integer-side stripe work
//
// These exist only for the hybrid kernel. Each is one instruction, and arm64's
// shifted-register operands make several of the steps free that cost a whole
// operation on the vector side.
// ---------------------------------------------------------------------------

// ldp loads a pair of adjacent 64-bit words.
func (a *arm64) ldp(d0, d1 GPR, base GPR, off int) {
	a.b.emit(func(m *Machine) {
		m.R[d0] = m.Load64(m.R[base] + uint64(off))
		m.R[d1] = m.Load64(m.R[base] + uint64(off) + 8)
	}, "ldp %s, %s, [%s, #%d]", a.GPRName(d0), a.GPRName(d1), a.GPRName(base), off)
}

func (a *arm64) stp(s0, s1 GPR, base GPR, off int) {
	a.b.emit(func(m *Machine) {
		m.Store64(m.R[base]+uint64(off), m.R[s0])
		m.Store64(m.R[base]+uint64(off)+8, m.R[s1])
	}, "stp %s, %s, [%s, #%d]", a.GPRName(s0), a.GPRName(s1), a.GPRName(base), off)
}

func (a *arm64) ldr(d GPR, base GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[d] = m.Load64(m.R[base] + uint64(off)) },
		"ldr %s, [%s, #%d]", a.GPRName(d), a.GPRName(base), off)
}

// eor3 computes dst = x ^ y.
func (a *arm64) eor3(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] ^ m.R[y] },
		"eor %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

// eorShr computes dst = x ^ (y >> sh), the whole xorshift in one instruction.
func (a *arm64) eorShr(dst, x, y GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] ^ (m.R[y] >> sh) },
		"eor %s, %s, %s, lsr #%d", a.GPRName(dst), a.GPRName(x), a.GPRName(y), sh)
}

func (a *arm64) lsr3(dst, src GPR, sh uint) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] >> sh },
		"lsr %s, %s, #%d", a.GPRName(dst), a.GPRName(src), sh)
}

// umaddl computes dst = addend + lo32(x)*lo32(y): the integer side's answer
// to NEON's umlal, and one instruction rather than two.
func (a *arm64) umaddl(dst, x, y, addend GPR) {
	a.b.emit(func(m *Machine) {
		m.R[dst] = m.R[addend] + uint64(uint32(m.R[x]))*uint64(uint32(m.R[y]))
	}, "umaddl %s, w%d, w%d, %s", a.GPRName(dst), int(x), int(y), a.GPRName(addend))
}

func (a *arm64) add3(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] + m.R[y] },
		"add %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64) mul3(dst, x, y GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[x] * m.R[y] },
		"mul %s, %s, %s", a.GPRName(dst), a.GPRName(x), a.GPRName(y))
}

func (a *arm64) zeroGPR(dst GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = 0 }, "mov %s, xzr", a.GPRName(dst))
}

// GroupBegin loads the scalar lanes' secret window. Everything after this
// rotates through it: at stripe k, lane 4+j reads ssec[(k+j) mod 4], and the
// register lane 4 has just finished with takes the word stripe k+4 will need.
// After four stripes the pointer has advanced four words and the mapping is
// back where it started, so the loop body maintains its own invariant.
func (a *arm64) GroupBegin(sec GPR) {
	if a.winE != nil {
		a.vloadPair(a.winE[0], a.winE[1], sec, 0)
		a.vload(a.winO[0], sec, 8)
		a.vload(a.winO[1], sec, 24)
	}
	if a.scalarLanes == 0 {
		return
	}
	base := 8 * (accNB - a.scalarLanes)
	for i := 0; i < a.scalarLanes; i += 2 {
		a.ldp(a.ssec[i], a.ssec[i+1], sec, base+8*i)
	}
}

// stripeScalarGroup is stripeScalar inside an unrolled group: the secret comes
// from the rotating window rather than memory, which is three of the four
// loads a stripe would otherwise make on this side.
func (a *arm64) stripeScalarGroup(k int, in GPR, inOff int, sec GPR, secOff int) {
	n := a.scalarLanes
	d0, d1, k0, k1 := a.stmp[0], a.stmp[1], a.stmp[2], a.stmp[3]
	for i := 0; i < n; i += 2 {
		off := 8 * (accNB - n + i)
		s0, s1 := a.ssec[(k+i)%n], a.ssec[(k+i+1)%n]
		a.ldp(d0, d1, in, inOff+off)
		a.eor3(k0, d0, s0)
		a.eor3(k1, d1, s1)
		if a.laneReassoc {
			// s0 is free now, and takes the word this lane will read n
			// stripes from here.
			a.ldr(s0, sec, secOff+8*accNB)
			a.laneMixReassoc(i, d0, d1, k0, k1)
			continue
		}
		a.add3(a.scc[i], a.scc[i], d1)
		a.add3(a.scc[i+1], a.scc[i+1], d0)
		if i == 0 {
			// s0 is free now, and takes the word this lane will read four
			// stripes from here.
			a.ldr(s0, sec, secOff+8*accNB)
		}
		a.lsr3(d0, k0, 32)
		a.lsr3(d1, k1, 32)
		a.umaddl(a.scc[i], k0, d0, a.scc[i])
		a.umaddl(a.scc[i+1], k1, d1, a.scc[i+1])
	}
}

// laneMixReassoc finishes a scalar lane pair with the product added to the
// neighbour's data word first and the accumulator last:
//
//	acc[i] += d[i+1] + lo(k[i]) * hi(k[i])
//
// grouped as acc[i] += (d[i+1] + lo*hi). Same six instructions as the
// straight form; what changes is the dependency chain through acc[i]. A
// multiply-accumulate whose addend comes from anything but another multiply
// waits the whole multiplier latency for it -- measured 4 cycles per stripe on
// an Apple M2 for add-then-umaddl against 1 for umaddl-then-umaddl -- and the
// straight form puts the lane's add right there. Grouped this way the chain
// through the accumulator is one add per stripe and the multiply hangs off the
// freshly loaded data instead. It costs two more temporaries, which only the
// two-lane split has to spare.
func (a *arm64) laneMixReassoc(i int, d0, d1, k0, k1 GPR) {
	h0, h1 := a.stmp[4], a.stmp[5]
	a.lsr3(h0, k0, 32)
	a.lsr3(h1, k1, 32)
	a.umaddl(d1, k0, h0, d1)
	a.umaddl(d0, k1, h1, d0)
	a.add3(a.scc[i], a.scc[i], d1)
	a.add3(a.scc[i+1], a.scc[i+1], d0)
}

// stripeScalar absorbs the lanes held in general-purpose registers, in the
// algorithm's own form: each lane takes its own keyed product and its
// neighbour's raw value. The pair is loaded together, and each data register
// is freed by the neighbour's add before being reused for the shift.
func (a *arm64) stripeScalar(in GPR, inOff int, sec GPR, secOff int) {
	for i := 0; i < a.scalarLanes; i += 2 {
		off := 8 * (accNB - a.scalarLanes + i)
		d0, d1, k0, k1 := a.stmp[0], a.stmp[1], a.stmp[2], a.stmp[3]
		a.ldp(d0, d1, in, inOff+off)
		a.ldp(k0, k1, sec, secOff+off)
		a.eor3(k0, d0, k0)
		a.eor3(k1, d1, k1)
		if a.laneReassoc {
			a.laneMixReassoc(i, d0, d1, k0, k1)
			continue
		}
		a.add3(a.scc[i], a.scc[i], d1)
		a.add3(a.scc[i+1], a.scc[i+1], d0)
		a.lsr3(d0, k0, 32)
		a.lsr3(d1, k1, 32)
		a.umaddl(a.scc[i], k0, d0, a.scc[i])
		a.umaddl(a.scc[i+1], k1, d1, a.scc[i+1])
	}
}

// swap64 exchanges the two 64-bit halves of each 128-bit group. NEON has a
// direct rotate; SVE has to build it from two transposes, but it happens once
// per block rather than once per stripe.
func (a *arm64) swap64(dst, src VReg) {
	sim := func(m *Machine) {
		var out [8]uint64
		for i := 0; i < a.lanes; i += 2 {
			out[i], out[i+1] = m.V[src][i+1], m.V[src][i]
		}
		m.V[dst] = out
	}
	if !a.sve {
		a.b.emit(sim, "ext %s, %s, %s, #8", a.v(dst, "16b"), a.v(src, "16b"), a.v(src, "16b"))
		return
	}
	t := a.tmp[len(a.tmp)-1]
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		for i := 0; i < a.lanes; i += 2 {
			out[i], out[i+1] = m.V[src][i+1], m.V[src][i+1]
		}
		m.V[dst] = out
	}, "trn2 %s, %s, %s", a.z(dst, "d"), a.z(src, "d"), a.z(src, "d"))
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		for i := 0; i < a.lanes; i += 2 {
			out[i], out[i+1] = m.V[src][i], m.V[src][i]
		}
		m.V[t] = out
	}, "trn1 %s, %s, %s", a.z(t, "d"), a.z(src, "d"), a.z(src, "d"))
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		for i := 0; i < a.lanes; i += 2 {
			out[i], out[i+1] = m.V[dst][i], m.V[t][i]
		}
		m.V[dst] = out
	}, "trn1 %s, %s, %s", a.z(dst, "d"), a.z(dst, "d"), a.z(t, "d"))
}

func (a *arm64) Setup(scramble bool) {
	if scramble {
		a.SetupScramble()
	}
	// SVE needs its governing predicate, whatever else it skips.
	if a.sve {
		a.b.emit(func(m *Machine) {}, "ptrue p0.d")
	}
}

// WriteOutTail is on for every arm64 backend that can address the secret
// at an offset, which is all but SVE; see TailWriter.
func (a *arm64) WriteOutTail() bool { return true }

// SetupScramble builds the scramble's multiplier: in x12 for the scalar
// lanes, and broadcast into kprime (and, for NEON, kprimeHi). hashLong
// builds it only where it reaches a block; see LateScrambleSetup.
func (a *arm64) SetupScramble() {
	const prime32_1 = 0x9E3779B1
	r := a.GPRName(armConstGPR)
	a.b.emit(func(m *Machine) { m.R[armConstGPR] = prime32_1 & 0xffff },
		"mov %s, #%d", r, prime32_1&0xffff)
	a.b.emit(func(m *Machine) { m.R[armConstGPR] |= prime32_1 &^ 0xffff },
		"movk %s, #%d, lsl #16", r, prime32_1>>16)
	if a.sve {
		// SVE2 has a 64-bit multiply, so the constant goes in whole.
		a.b.emit(func(m *Machine) {
			for i := 0; i < a.lanes; i++ {
				m.V[a.kprime][i] = m.R[armConstGPR]
			}
		}, "mov %s, %s", a.z(a.kprime, "d"), r)
		return
	}
	a.b.emit(func(m *Machine) {
		c := m.R[armConstGPR] & 0xffffffff
		for i := 0; i < a.lanes; i++ {
			m.V[a.kprime][i] = c | c<<32
		}
	}, "dup %s, w%d", a.v(a.kprime, "4s"), int(armConstGPR))
	// kprimeHi is {0, PRIME32_1} in each 64-bit lane: what the scramble
	// multiplies the accumulator's 32-bit halves by to get the high half of
	// the product in place with the low half already zeroed. See Scramble.
	a.vshl(a.kprimeHi, a.kprime, 32)
}

// vshl shifts each 64-bit lane left.
func (a *arm64) vshl(dst, src VReg, sh uint) {
	a.b.emit(func(m *Machine) {
		for i := 0; i < a.lanes; i++ {
			m.V[dst][i] = m.V[src][i] << sh
		}
	}, "shl %s, %s, #%d", a.v(dst, "2d"), a.v(src, "2d"), sh)
}

// xtn narrows the 64-bit lanes to their low 32 bits, packed into the low
// half of dst.
func (a *arm64) xtn(dst, src VReg) {
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		out[0] = uint64(uint32(m.V[src][0])) | uint64(uint32(m.V[src][1]))<<32
		m.V[dst] = out
	}, "xtn %s, %s", a.v(dst, "2s"), a.v(src, "2d"))
}

// shrn narrows the 64-bit lanes to their bits sh and up, packed into the low
// half of dst; at sh = 32 that is the high halves.
func (a *arm64) shrn(dst, src VReg, sh uint) {
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		out[0] = uint64(uint32(m.V[src][0]>>sh)) | uint64(uint32(m.V[src][1]>>sh))<<32
		m.V[dst] = out
	}, "shrn %s, %s, #%d", a.v(dst, "2s"), a.v(src, "2d"), sh)
}

// mul4s multiplies 32-bit lanes, keeping the low 32 bits of each product.
func (a *arm64) mul4s(dst, x, y VReg) {
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		for i := 0; i < a.lanes; i++ {
			lo := uint32(m.V[x][i]) * uint32(m.V[y][i])
			hi := uint32(m.V[x][i]>>32) * uint32(m.V[y][i]>>32)
			out[i] = uint64(lo) | uint64(hi)<<32
		}
		m.V[dst] = out
	}, "mul %s, %s, %s", a.v(dst, "4s"), a.v(x, "4s"), a.v(y, "4s"))
}

func (a *arm64) Finish() {}

// LoadAcc reads in whatever width the register file has; a table and a
// caller's array are the same to it, so constant is unused here.
func (a *arm64) LoadAcc(p GPR, constant bool) {
	for i := 0; i < a.nvec; i++ {
		a.vload(a.accA[i], p, a.vl*i)
		a.vzero(a.accB[i])
	}
	for i := 0; i < a.scalarLanes; i += 2 {
		a.ldp(a.scc[i], a.scc[i+1], p, 8*(accNB-a.scalarLanes+i))
	}
}

func (a *arm64) StoreAcc(p GPR) {
	for i := 0; i < a.nvec; i++ {
		a.vstore(a.accA[i], p, a.vl*i)
	}
	for i := 0; i < a.scalarLanes; i += 2 {
		a.stp(a.scc[i], a.scc[i+1], p, 8*(accNB-a.scalarLanes+i))
	}
}

// ---------------------------------------------------------------------------
// The seeded kernel; see DerivedSeedArch
// ---------------------------------------------------------------------------

// SecretGPR is x4: hashLong's limit register, which the four-argument
// seeded kernel does not take as an argument, and which is free until the
// derivation has read the default secret through it.
func (a *arm64) SecretGPR() GPR { return 4 }

// spGPR is the register number the stack pointer has in the instructions
// that can name it; the simulator models it as R[31].
const spGPR GPR = 31

// FrameSecret points dst at the frame's secret buffer. The Go assembler puts
// a frame's locals at RSP+8, so RSP+16 is the first 16-byte-aligned address
// in them.
func (a *arm64) FrameSecret(dst GPR) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[spGPR] + 16 },
		"add %s, sp, #16", a.GPRName(dst))
}

// DeriveSecret derives with NEON whatever the backend: the secret is 192
// bytes of memory to memory, which a vector width does not change, and NEON
// can pair its loads and stores where SVE's offsets are counted in vector
// lengths. Every 16-byte piece of the secret starts at an even word, so one
// pattern, [seed, -seed], keys all twelve. The registers are the stripe
// loop's scratch, which nothing holds yet; the scramble constant Setup has
// already built is in v14 and v15, below them.
func (a *arm64) DeriveSecret(dst, src, seed GPR) {
	neg := a.TmpGPR(1)
	p := VReg(28)
	a.b.emit(func(m *Machine) { m.R[neg] = -m.R[seed] },
		"neg %s, %s", a.GPRName(neg), a.GPRName(seed))
	a.b.emit(func(m *Machine) { m.V[p] = [8]uint64{m.R[seed]} },
		"fmov d%d, %s", int(p), a.GPRName(seed))
	a.b.emit(func(m *Machine) { m.V[p][1] = m.R[neg] },
		"mov %s[1], %s", a.v(p, "d"), a.GPRName(neg))
	const pieces = secretDefaultSize / 16
	for i := 0; i < pieces; i += 2 {
		a.ldpq(VReg(16+i), VReg(17+i), src, 16*i)
	}
	for i := 0; i < pieces; i++ {
		r := VReg(16 + i)
		a.b.emit(func(m *Machine) {
			m.V[r] = [8]uint64{m.V[r][0] + m.V[p][0], m.V[r][1] + m.V[p][1]}
		}, "add %s, %s, %s", a.v(r, "2d"), a.v(r, "2d"), a.v(p, "2d"))
	}
	for i := 0; i < pieces; i += 2 {
		a.stpq(VReg(16+i), VReg(17+i), dst, 16*i)
	}
}

// ldpq and stpq are the NEON pair load and store whatever the backend, with
// the NEON semantics: two 64-bit lanes per register, the rest zeroed.
func (a *arm64) ldpq(d0, d1 VReg, r GPR, off int) {
	a.b.emit(func(m *Machine) {
		m.V[d0] = m.LoadVec(m.R[r]+uint64(off), 2)
		m.V[d1] = m.LoadVec(m.R[r]+uint64(off)+16, 2)
	}, "ldp %s, %s, [%s, #%d]", a.q(d0), a.q(d1), a.GPRName(r), off)
}

func (a *arm64) stpq(s0, s1 VReg, r GPR, off int) {
	a.b.emit(func(m *Machine) {
		m.StoreVec(m.R[r]+uint64(off), m.V[s0], 2)
		m.StoreVec(m.R[r]+uint64(off)+16, m.V[s1], 2)
	}, "stp %s, %s, [%s, #%d]", a.q(s0), a.q(s1), a.GPRName(r), off)
}

// ---------------------------------------------------------------------------
// The merging one-shot kernels; see MergeArch
// ---------------------------------------------------------------------------

// LongStart sets dst = n * table[slot], or its complement.
func (a *arm64) LongStart(dst, n GPR, slot int, not bool) {
	t := a.TableGPR()
	a.b.emit(func(m *Machine) { m.R[dst] = m.Load64(m.R[t] + uint64(8*slot)) },
		"ldr %s, [%s, #%d]", a.GPRName(dst), a.GPRName(t), 8*slot)
	a.b.emit(func(m *Machine) { m.R[dst] *= m.R[n] },
		"mul %s, %s, %s", a.GPRName(dst), a.GPRName(dst), a.GPRName(n))
	if not {
		a.b.emit(func(m *Machine) { m.R[dst] = ^m.R[dst] },
			"mvn %s, %s", a.GPRName(dst), a.GPRName(dst))
	}
}

// KeyAt is dst = src + off, one add.
func (a *arm64) KeyAt(dst, src GPR, off int) {
	a.b.emit(func(m *Machine) { m.R[dst] = m.R[src] + uint64(off) },
		"add %s, %s, #%d", a.GPRName(dst), a.GPRName(src), off)
}

func (a *arm64) StorePair(lo, hi, p GPR) { a.stp(lo, hi, p, 0) }

// mergeScratch is every general-purpose register a kernel's end may use
// for the merge, in the order it takes them: the stripe loop's
// temporaries, the scramble constant, the scalar lanes' secret window and
// scratch, and the argument registers past the fourth.
var mergeScratch = []GPR{5, 6, 7, 8, 9, 10, 11, 12, 22, 23, 24, 25, 13, 14, 15, 16}

// MergeLong keys the accumulators, moves the vector lanes into
// general-purpose registers and folds them there. NEON keys its lanes as
// vectors first, one xor a register pair against two a lane pair, and moves
// the keyed lanes across; SVE moves each 128-bit quarter of a register down
// to where NEON can read it and keys the lanes as integers. The scalar
// lanes are in general-purpose registers already.
func (a *arm64) MergeLong(ret, start, key GPR, keep ...GPR) {
	busy := map[GPR]bool{ret: true, start: true, key: true, a.TableGPR(): true}
	for _, r := range keep {
		busy[r] = true
	}
	for _, r := range a.scc {
		busy[r] = true
	}
	var free []GPR
	for _, r := range mergeScratch {
		if !busy[r] {
			free = append(free, r)
		}
	}
	take := func() GPR {
		if len(free) == 0 {
			panic("asmgen: MergeLong ran out of registers")
		}
		r := free[0]
		free = free[1:]
		return r
	}
	// move puts the two 64-bit lanes of NEON register t in l0 and l1.
	move := func(t VReg) (GPR, GPR) {
		l0, l1 := take(), take()
		a.b.emit(func(m *Machine) { m.R[l0] = m.V[t][0] }, "fmov %s, d%d", a.GPRName(l0), int(t))
		a.b.emit(func(m *Machine) { m.R[l1] = m.V[t][1] }, "mov %s, %s[1]", a.GPRName(l1), a.v(t, "d"))
		return l0, l1
	}
	var lane [accNB]GPR
	vlanes := accNB - a.scalarLanes
	if !a.sve {
		for j := 0; j < a.nvec; j += 2 {
			t0 := a.tmp[j]
			if j+1 < a.nvec {
				t1 := a.tmp[j+1]
				a.vloadPair(t0, t1, key, 16*j)
				a.vxor(t0, t0, a.accA[j])
				a.vxor(t1, t1, a.accA[j+1])
				lane[2*j], lane[2*j+1] = move(t0)
				lane[2*j+2], lane[2*j+3] = move(t1)
				continue
			}
			a.vload(t0, key, 16*j)
			a.vxor(t0, t0, a.accA[j])
			lane[2*j], lane[2*j+1] = move(t0)
		}
	} else {
		t := a.tmp[0]
		for j := 0; j < a.nvec; j++ {
			for c := 0; c < a.lanes/2; c++ {
				src := a.accA[j]
				if c > 0 {
					c, acc := c, a.accA[j]
					a.b.emit(func(m *Machine) {
						for i := 0; i < a.lanes; i += 2 {
							m.V[t][i], m.V[t][i+1] = m.V[acc][2*c], m.V[acc][2*c+1]
						}
					}, "dup %s, %s[%d]", a.z(t, "q"), a.z(acc, "q"), c)
					src = t
				}
				w := a.lanes*j + 2*c
				lane[w], lane[w+1] = move(src)
			}
		}
		for w := 0; w < vlanes; w += 2 {
			k0, k1 := take(), take()
			a.ldp(k0, k1, key, 8*w)
			a.eor3(lane[w], lane[w], k0)
			a.eor3(lane[w+1], lane[w+1], k1)
			free = append(free, k0, k1)
		}
	}
	for i := 0; i < a.scalarLanes; i += 2 {
		w := vlanes + i
		k0, k1 := take(), take()
		a.ldp(k0, k1, key, 8*w)
		a.eor3(k0, k0, a.scc[i])
		a.eor3(k1, k1, a.scc[i+1])
		lane[w], lane[w+1] = k0, k1
	}
	// The four folds, lo ^ hi of each lane pair's product, then their sum
	// from start as a tree, then the avalanche.
	h := take()
	var f [4]GPR
	for i := 0; i < 4; i++ {
		lo, hi := lane[2*i], lane[2*i+1]
		f[i] = lo
		a.b.emit(func(m *Machine) { m.R[h] = mulHigh(m.R[lo], m.R[hi]) },
			"umulh %s, %s, %s", a.GPRName(h), a.GPRName(lo), a.GPRName(hi))
		a.mul3(lo, lo, hi)
		a.eor3(lo, lo, h)
	}
	a.add3(f[1], f[1], f[2])
	a.add3(f[0], f[0], f[3])
	a.add3(ret, start, f[1])
	a.add3(ret, ret, f[0])
	a.eorShr(ret, ret, ret, 37)
	c, t := h, a.TableGPR()
	a.b.emit(func(m *Machine) { m.R[c] = m.Load64(m.R[t] + 8*longSlotAvalanche) },
		"ldr %s, [%s, #%d]", a.GPRName(c), a.GPRName(t), 8*longSlotAvalanche)
	a.mul3(ret, ret, c)
	a.eorShr(ret, ret, ret, 32)
}

// No arm64 backend keeps the secret schedule in registers. NEON and the
// shorter SVE lengths spend two or four registers per stripe, so a block's
// worth does not fit; only SVE2 at a 512-bit vector length could, and that
// cannot be measured on the hardware this was tuned on.
func (a *arm64) FastBlockStripes() int    { return 0 }
func (a *arm64) LoadSecretRegs(GPR)       { panic("asmgen: no arm64 fast block") }
func (a *arm64) FastStripe(int, GPR, int) { panic("asmgen: no arm64 fast block") }

// Stripe absorbs 64 bytes. Only the scalar side cares whether it is part of an
// unrolled group; the vector side just wants an index to rotate scratch
// registers by, and a standalone stripe can use the first set.
func (a *arm64) Stripe(k int, in GPR, inOff int, sec GPR, secOff int) {
	grouped := k >= 0
	if !grouped {
		k = 0
	}
	if a.sve {
		a.stripeSVE(k, in, inOff, sec, secOff)
		return
	}
	a.stripeNEON(k, grouped, in, inOff, sec, secOff)
	switch {
	case a.scalarLanes == 0:
	case grouped:
		a.stripeScalarGroup(k, in, inOff, sec, secOff)
	default:
		a.stripeScalar(in, inOff, sec, secOff)
	}
}

// stripeNEON works on register pairs, because uzp1/uzp2 deinterleave two
// registers in one instruction each.
func (a *arm64) stripeNEON(k int, grouped bool, in GPR, inOff int, sec GPR, secOff int) {
	if a.winE != nil && grouped {
		a.stripeNEONWindow(k, in, inOff, sec, secOff)
		return
	}
	if a.sec8 != 0 && grouped {
		if k == 0 {
			// The first stripe of the group sets the odd stripes' base; the
			// group's secret pointer does not move until its end.
			a.b.emit(func(m *Machine) { m.R[a.sec8] = m.R[sec] + 8 },
				"add %s, %s, #8", a.GPRName(a.sec8), a.GPRName(sec))
		}
		if secOff%16 == 8 {
			sec, secOff = a.sec8, secOff-8
		}
	}
	for j := 0; j < a.nvec; j += 2 {
		if j+1 == a.nvec {
			// An odd register count leaves one register without a partner
			// for uzp. Its halves are split the way the reference does it,
			// with xtn and shrn: five operations for the register against the
			// pair's four each, which is still cheaper than a fourth vector
			// register would be on a core whose integer side has room for two
			// more lanes.
			t := a.tmp[12:]
			d, s, lo, hi := t[0], t[1], t[2], t[3]
			a.vload(d, in, inOff+16*j)
			a.vload(s, sec, secOff+16*j)
			a.vxor(s, d, s)
			a.xtn(lo, s)
			a.shrn(hi, s, 32)
			a.umlalNEON(a.accA[j], lo, hi, false)
			a.vadd(a.accB[j], a.accB[j], d)
			return
		}
		t := a.tmp[6*((k*a.nvec/2+j/2)%2):]
		d0, d1, s0, s1, lo, hi := t[0], t[1], t[2], t[3], t[4], t[5]
		a.vloadPair(d0, d1, in, inOff+16*j)
		a.vloadPair(s0, s1, sec, secOff+16*j)
		a.vxor(s0, d0, s0)
		a.vxor(s1, d1, s1)
		a.uzp(lo, s0, s1, false)
		a.uzp(hi, s0, s1, true)
		a.umlalNEON(a.accA[j], lo, hi, false)
		a.umlalNEON(a.accA[j+1], lo, hi, true)
		a.vadd(a.accB[j], a.accB[j], d0)
		a.vadd(a.accB[j+1], a.accB[j+1], d1)
	}
}

// stripeNEONWindow is stripeNEON for a two-register stripe inside an
// unrolled group, with its keys from the window rather than from memory.
//
// Stripe k reads the four secret words from k: two 16-byte keys, which for
// an even k start at even words and for an odd k at odd ones. Consecutive
// stripes of one parity share a key -- stripe k's second is stripe k+2's
// first -- so each parity keeps a window of two, and each stripe loads the
// one key it does not share, into the register of the one it has just
// finished with. That is one 16-byte load a stripe where the pair was two
// on the even stripes and two unaligned ones on the odd, and on a Neoverse
// N2, which issues this loop as fast as it dispatches it, an ldp of two
// vectors dispatches as two operations like the loads it replaces: the
// window saves an operation a stripe, and measured 3.7% of the loop in a
// probe of it. The load reaches four words past the stripe, which a
// secret always has: hashLong's own window for the scalar lanes reaches
// eight.
//
// The window turns over two keys a parity every four stripes, so it needs
// an unroll that is a multiple of four to come back to the register it
// started in; GroupBegin fills it.
func (a *arm64) stripeNEONWindow(k int, in GPR, inOff int, sec GPR, secOff int) {
	if a.nvec != 2 || a.unroll%4 != 0 {
		panic("asmgen: the key window is for a two-register stripe and an unroll of a multiple of four")
	}
	win := a.winE
	if k%2 == 1 {
		win = a.winO
	}
	ka, kb := win[(k/2)%2], win[(k/2+1)%2]
	t := a.tmp[6*(k%2):]
	d0, d1, x0, x1, lo, hi := t[0], t[1], t[2], t[3], t[4], t[5]
	a.vloadPair(d0, d1, in, inOff)
	a.vxor(x0, d0, ka)
	a.vxor(x1, d1, kb)
	// ka is done with: it takes the key two stripes of this parity on,
	// which starts four words past this stripe's.
	a.vload(ka, sec, secOff+32)
	a.uzp(lo, x0, x1, false)
	a.uzp(hi, x0, x1, true)
	a.umlalNEON(a.accA[0], lo, hi, false)
	a.umlalNEON(a.accA[1], lo, hi, true)
	a.vadd(a.accB[0], a.accB[0], d0)
	a.vadd(a.accB[1], a.accB[1], d1)
}

func (a *arm64) stripeSVE(k int, in GPR, inOff int, sec GPR, secOff int) {
	for j := 0; j < a.nvec; j++ {
		t := a.tmp[3*((k*a.nvec+j)%(len(a.tmp)/3)):]
		d, key, h := t[0], t[1], t[2]
		a.vload(d, in, inOff+a.vl*j)
		a.vload(key, sec, secOff+a.vl*j)
		a.vxor(key, d, key)
		a.vshr(h, key, 32)
		a.umlalbSVE(a.accA[j], key, h)
		a.vadd(a.accB[j], a.accB[j], d)
	}
}

func (a *arm64) Materialize(final bool) {
	t := a.tmp[0]
	for j := 0; j < a.nvec; j++ {
		a.swap64(t, a.accB[j])
		a.vadd(a.accA[j], a.accA[j], t)
		if !final {
			a.vzero(a.accB[j])
		}
	}
	// The scalar lanes need nothing here: they were never split.
}

// Scramble applies acc = (xorshift(acc,47) ^ secret) * PRIME32_1. SVE2 has a
// 64-bit vector multiply; NEON does not, so it assembles the product from the
// two 32x32 halves. The integer side has both a shifted-operand xor and a real
// 64-bit multiply, so it needs four instructions per lane against six.
func (a *arm64) Scramble(sec GPR, secOff int) {
	for i := 0; i < a.scalarLanes; i++ {
		acc, t := a.scc[i], a.stmp[0]
		off := secOff + 8*(accNB-a.scalarLanes+i)
		a.eorShr(acc, acc, acc, 47)
		a.ldr(t, sec, off)
		a.eor3(acc, acc, t)
		a.mul3(acc, acc, armConstGPR)
	}
	for j := 0; j < a.nvec; j++ {
		acc := a.accA[j]
		t := a.tmp[0]
		a.vshr(t, acc, 47)
		a.vxor(acc, acc, t)
		a.vload(t, sec, secOff+a.vl*j)
		a.vxor(acc, acc, t)
		if a.sve {
			a.b.emit(func(m *Machine) {
				for i := 0; i < a.lanes; i++ {
					m.V[acc][i] *= m.V[a.kprime][i]
				}
			}, "mul %s, %s, %s", a.z(acc, "d"), a.z(acc, "d"), a.z(a.kprime, "d"))
			continue
		}
		// acc * P over 64 bits, with only a 32x32 multiplier: the product is
		// lo(acc)*P in full plus hi(acc)*P shifted up 32, of which only the low
		// 32 bits survive. A 32-bit lane multiply by {0, P} produces exactly
		// that high part with the low lane already zero, so it can serve as
		// the accumulator for the widening multiply-add of the low halves.
		// Three operations against the six of doing it with two umull and a
		// shift and add. It is the reference implementation's own NEON form.
		a.xtn(t, acc)
		a.mul4s(acc, acc, a.kprimeHi)
		a.umlalNEON(acc, t, a.kprime, false)
	}
}
