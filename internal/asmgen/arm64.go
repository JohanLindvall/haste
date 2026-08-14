package asmgen

import "fmt"

// arm64 has two backends. NEON is unconditional: every arm64 CPU has it, so it
// is the baseline rather than an upgrade. SVE2 is generated once per vector
// length, because a stripe is a fixed 64 bytes and how many registers that
// occupies is exactly what SVE leaves unspecified; the dispatcher reads the
// hardware's length and picks the matching kernel.

// Go's arm64 ABI reserves R18 (platform), R27 (assembler temporary), R28
// (goroutine), R29 (frame pointer) and R30 (link register); none appear here.
var armArgGPR = []GPR{0, 1, 2, 3, 4, 5}
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

	accA, accB []VReg
	tmp        []VReg
	kprime     VReg
}

// newNEON builds the NEON kernel, which is the arm64 baseline.
//
// Measured on a two-vector-pipe core (Neoverse N2), this loop runs at about
// 10.9 cycles per 64-byte stripe against a marginal rate of ~1.9 vector
// operations per cycle. Moving half the stripe onto the idle integer pipes
// measured out at roughly 7 cycles there, but it costs instruction bandwidth,
// which would make it a regression on four-pipe cores (Neoverse V-series,
// Apple), so the kernel stays purely vector.
func newNEON(unroll int) *arm64 { return newARM("neon", false, 16, unroll) }

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
	return a
}

func (a *arm64) Name() string     { return a.name }
func (a *arm64) GOARCH() string   { return "arm64" }
func (a *arm64) Build() *Builder  { return a.b }
func (a *arm64) Unroll() int      { return a.unroll }
func (a *arm64) SecretImm() bool  { return !a.sve }
func (a *arm64) ArgGPR(i int) GPR { return armArgGPR[i] }
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

var armCC = map[Cond]string{LT: "lt", GE: "ge", EQ: "eq", NE: "ne", GT: "gt", LE: "le"}

func (a *arm64) BranchI(r GPR, imm int64, c Cond, label string) {
	a.b.emit(func(m *Machine) { m.setCmp(m.R[r], uint64(imm)) },
		"cmp %s, #%d", a.GPRName(r), imm)
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

func (a *arm64) Setup() {
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
		a.b.emit(func(m *Machine) {}, "ptrue p0.d")
		return
	}
	a.b.emit(func(m *Machine) {
		c := m.R[armConstGPR] & 0xffffffff
		for i := 0; i < a.lanes; i++ {
			m.V[a.kprime][i] = c | c<<32
		}
	}, "dup %s, w%d", a.v(a.kprime, "4s"), int(armConstGPR))
}

func (a *arm64) Finish() {}

func (a *arm64) LoadAcc(p GPR) {
	for i := 0; i < a.nvec; i++ {
		a.vload(a.accA[i], p, a.vl*i)
		a.vzero(a.accB[i])
	}
}

func (a *arm64) StoreAcc(p GPR) {
	for i := 0; i < a.nvec; i++ {
		a.vstore(a.accA[i], p, a.vl*i)
	}
}

// Stripe absorbs 64 bytes.
func (a *arm64) Stripe(k int, in GPR, inOff int, sec GPR, secOff int) {
	if a.sve {
		a.stripeSVE(k, in, inOff, sec, secOff)
		return
	}
	a.stripeNEON(k, in, inOff, sec, secOff)
}

// stripeNEON works on register pairs, because uzp1/uzp2 deinterleave two
// registers in one instruction each.
func (a *arm64) stripeNEON(k int, in GPR, inOff int, sec GPR, secOff int) {
	for j := 0; j < a.nvec; j += 2 {
		t := a.tmp[6*((k*a.nvec/2+j/2)%(len(a.tmp)/6)):]
		d0, d1, s0, s1, lo, hi := t[0], t[1], t[2], t[3], t[4], t[5]
		a.vload(d0, in, inOff+16*j)
		a.vload(d1, in, inOff+16*j+16)
		a.vload(s0, sec, secOff+16*j)
		a.vload(s1, sec, secOff+16*j+16)
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

func (a *arm64) Materialize() {
	t := a.tmp[0]
	for j := 0; j < a.nvec; j++ {
		a.swap64(t, a.accB[j])
		a.vadd(a.accA[j], a.accA[j], t)
		a.vzero(a.accB[j])
	}
}

// Scramble applies acc = (xorshift(acc,47) ^ secret) * PRIME32_1. SVE2 has a
// 64-bit vector multiply; NEON does not, so it assembles the product from the
// two 32x32 halves.
func (a *arm64) Scramble(sec GPR, secOff int) {
	for j := 0; j < a.nvec; j++ {
		acc := a.accA[j]
		t, u, w := a.tmp[0], a.tmp[1], a.tmp[2]
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
		// t = low halves, u = high halves, both as 2x32 in the low 64 bits.
		a.b.emit(func(m *Machine) {
			var out [8]uint64
			out[0] = uint64(uint32(m.V[acc][0])) | uint64(uint32(m.V[acc][1]))<<32
			m.V[t] = out
		}, "xtn %s, %s", a.v(t, "2s"), a.v(acc, "2d"))
		a.b.emit(func(m *Machine) {
			var out [8]uint64
			out[0] = uint64(uint32(m.V[acc][0]>>32)) | uint64(uint32(m.V[acc][1]>>32))<<32
			m.V[u] = out
		}, "shrn %s, %s, #32", a.v(u, "2s"), a.v(acc, "2d"))
		a.umullNEON(w, t, a.kprime)
		a.umullNEON(u, u, a.kprime)
		a.b.emit(func(m *Machine) {
			for i := 0; i < 2; i++ {
				m.V[u][i] <<= 32
			}
		}, "shl %s, %s, #32", a.v(u, "2d"), a.v(u, "2d"))
		a.vadd(acc, w, u)
	}
}

// umullNEON computes dst = lo32 elements of x times those of y, widened.
func (a *arm64) umullNEON(dst, x, y VReg) {
	a.b.emit(func(m *Machine) {
		var out [8]uint64
		for i := 0; i < 2; i++ {
			l := uint32(m.V[x][0] >> (32 * i))
			r := uint32(m.V[y][0] >> (32 * i))
			out[i] = uint64(l) * uint64(r)
		}
		m.V[dst] = out
	}, "umull %s, %s, %s", a.v(dst, "2d"), a.v(x, "2s"), a.v(y, "2s"))
}
