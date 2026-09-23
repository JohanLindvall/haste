package asmgen

// The seeded one-shot kernel on x86; see SeededArch for what it is and why.
//
// The seed pattern is P = [seed, -seed, seed, -seed, ...], the difference
// between the derived secret and the default one over a run of words that
// starts at an even index; a run that starts at an odd index differs by -P.
// Each backend keeps P, and -P where it has a register for it:
//
//	SSE2     P in xmm15; an odd run subtracts P instead, one psubq for a paddq
//	AVX2     P in ymm13, -P in ymm15
//	AVX-512  P in zmm15, -P in zmm31
//
// None of those is otherwise used: the scramble's multiplier is register 14
// and every pool stops below it -- AVX2's scratch pool at ymm12 -- and 15 is
// X15, which an ABI0 kernel may clobber. zmm31 is the last of the sixteen
// registers the fast block loop fills with a block's secret schedule, so it
// is rebuilt from zmm15 when that loop is done with it.

func (x *x86) seedP() VReg {
	switch x.mode {
	case modeSSE2:
		return VReg(15)
	case modeAVX2:
		return VReg(13)
	default:
		return VReg(15)
	}
}

// seedNP is the register holding -P, or -1 on SSE2, which has none spare.
func (x *x86) seedNP() VReg {
	switch x.mode {
	case modeSSE2:
		return -1
	case modeAVX2:
		return VReg(15)
	default:
		return VReg(31)
	}
}

// SecretGPR is r8: hashLong's limit register, free until the seed has been
// spent, and not an argument of the four-argument seeded kernel.
func (x *x86) SecretGPR() GPR { return r8 }

// xmm names a register at 128 bits whatever the backend's width, for the
// instructions that build the pattern.
func xmm(v VReg) string { return "%xmm" + itoa(int(v)) }

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func (x *x86) SetupSeed(r GPR) {
	neg := r10
	x.MovRI(neg, 0)
	x.SubRR(neg, r)
	p, np := x.seedP(), x.seedNP()
	// lo128 sets a register's low two lanes from two GPRs and zeroes the rest,
	// the way a 128-bit VEX or legacy write leaves it.
	switch x.mode {
	case modeSSE2:
		t := x.tmp[0]
		x.b.emit(func(m *Machine) { m.V[p] = [8]uint64{m.R[r]} }, "movq %s, %s", x.GPRName(r), xmm(p))
		x.b.emit(func(m *Machine) { m.V[t] = [8]uint64{m.R[neg]} }, "movq %s, %s", x.GPRName(neg), xmm(t))
		x.b.emit(func(m *Machine) { m.V[p][1] = m.V[t][0] }, "punpcklqdq %s, %s", xmm(t), xmm(p))
	case modeAVX2:
		x.b.emit(func(m *Machine) { m.V[p] = [8]uint64{m.R[r]} }, "vmovq %s, %s", x.GPRName(r), xmm(p))
		x.b.emit(func(m *Machine) { m.V[np] = [8]uint64{m.R[neg]} }, "vmovq %s, %s", x.GPRName(neg), xmm(np))
		x.b.emit(func(m *Machine) { m.V[p] = [8]uint64{m.V[p][0], m.V[np][0]} },
			"vpunpcklqdq %s, %s, %s", xmm(np), xmm(p), xmm(p))
		x.b.emit(func(m *Machine) { m.V[p][2], m.V[p][3] = m.V[p][0], m.V[p][1] },
			"vinserti128 $1, %s, %s, %s", xmm(p), x.vec(p), x.vec(p))
		x.vshuf32(np, p, 0x4e)
	default:
		x.b.emit(func(m *Machine) {
			for i := 0; i < x.lanes; i++ {
				m.V[p][i] = m.R[r]
			}
		}, "vpbroadcastq %s, %s", x.GPRName(r), x.vec(p))
		x.b.emit(func(m *Machine) {
			for i := 0; i < x.lanes; i++ {
				m.V[np][i] = m.R[neg]
			}
		}, "vpbroadcastq %s, %s", x.GPRName(neg), x.vec(np))
		x.b.emit(func(m *Machine) {
			for i := 0; i < x.lanes; i += 2 {
				m.V[p][i+1] = m.V[np][i]
			}
		}, "vpunpcklqdq %s, %s, %s", x.vec(np), x.vec(p), x.vec(p))
		x.vshuf32(np, p, 0x4e)
	}
}

func (x *x86) RefreshSeed() {
	if len(x.secretRegs) > 0 {
		x.vshuf32(x.seedNP(), x.seedP(), 0x4e)
	}
}

// seedKey loads the derived secret's words at [r+off] into dst: the default
// secret's plus P for a run starting at an even word, minus P for an odd one.
func (x *x86) seedKey(dst VReg, r GPR, off int, odd bool) {
	p := x.seedP()
	if x.mode == modeSSE2 {
		x.vload(dst, r, off)
		if odd {
			x.b.emit(func(m *Machine) {
				for i := 0; i < x.lanes; i++ {
					m.V[dst][i] -= m.V[p][i]
				}
			}, "psubq %s, %s", x.vec(p), x.vec(dst))
			return
		}
		x.b.emit(func(m *Machine) {
			for i := 0; i < x.lanes; i++ {
				m.V[dst][i] += m.V[p][i]
			}
		}, "paddq %s, %s", x.vec(p), x.vec(dst))
		return
	}
	if odd {
		p = x.seedNP()
	}
	x.b.emit(func(m *Machine) {
		v := m.LoadVec(m.R[r]+uint64(off), x.lanes)
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] = v[i] + m.V[p][i]
		}
	}, "vpaddq %s, %s, %s", x.mem(r, off), x.vec(p), x.vec(dst))
}

func (x *x86) vor(dst, a, b VReg) {
	x.vecOp("por", "vpor", "vporq", dst, a, b, func(m *Machine, i int) {
		m.V[dst][i] = m.V[a][i] | m.V[b][i]
	})
}

// vxor3 computes dst ^= b ^ c in one ternary-logic instruction.
func (x *x86) vxor3(dst, b, c VReg) {
	if x.mode != modeAVX {
		panic("asmgen: vxor3 needs AVX-512")
	}
	x.b.emit(func(m *Machine) {
		for i := 0; i < x.lanes; i++ {
			m.V[dst][i] ^= m.V[b][i] ^ m.V[c][i]
		}
	}, "vpternlogq $0x96, %s, %s, %s", x.vec(c), x.vec(b), x.vec(dst))
}

func (x *x86) LoadSecretRegsSeeded(sec GPR) {
	// The last register filled is -P's own, which every earlier one may
	// still need: an odd k reads -P.
	for k, r := range x.secretRegs {
		x.seedKey(r, sec, secretConsumeRate*k, k%2 == 1)
	}
}

// StripeSeeded is Stripe with the key built by seedKey. The vectors of one
// stripe start 2, 4 or 8 words apart, so they share the stripe's parity.
func (x *x86) StripeSeeded(k int, in GPR, inOff int, sec GPR, secOff int, odd bool) {
	w := x.mode / 8
	if k < 0 {
		k = 0
	}
	x.prefetchIn(in, inOff)
	for j := 0; j < x.nvec; j++ {
		t := x.tmp[3*((k*x.nvec+j)%(len(x.tmp)/3)):]
		d, key, h := t[0], t[1], t[2]
		x.vload(d, in, inOff+w*j)
		x.seedKey(key, sec, secOff+w*j, odd)
		x.vxor(key, key, d)
		x.hi32(h, key)
		x.vmul32(key, key, h)
		x.vadd(x.accA[j], x.accA[j], key)
		x.vadd(x.accB[j], x.accB[j], d)
	}
}

// FinalStripeSeeded keys the final stripe with the derived secret's bytes
// starting one byte into the odd word at [sec+secOff]: each 64-bit lane of
// that key is the top seven bytes of one derived word and the bottom byte of
// the next, which is the odd-phase run shifted down a byte and the even-phase
// run after it shifted up seven.
func (x *x86) FinalStripeSeeded(in GPR, inOff int, sec GPR, secOff int) {
	w := x.mode / 8
	for j := 0; j < x.nvec; j++ {
		d, key, h := x.tmp[0], x.tmp[1], x.tmp[2]
		x.vload(d, in, inOff+w*j)
		x.seedKey(key, sec, secOff+w*j, true)
		x.seedKey(h, sec, secOff+secretConsumeRate+w*j, false)
		x.vshr(key, key, 8)
		x.vshl(h, h, 56)
		x.vor(key, key, h)
		x.vxor(key, key, d)
		x.hi32(h, key)
		x.vmul32(key, key, h)
		x.vadd(x.accA[j], x.accA[j], key)
		x.vadd(x.accB[j], x.accB[j], d)
	}
}

// ScrambleSeeded is Scramble keyed by seedKey, at the even word the default
// secret's limit falls on.
func (x *x86) ScrambleSeeded(sec GPR, secOff int) {
	w := x.mode / 8
	for j := 0; j < x.nvec; j++ {
		a := x.accA[j]
		t, hi, lo := x.tmp[0], x.tmp[1], x.tmp[2]
		x.vshr(t, a, 47)
		if x.mode == modeAVX {
			x.seedKey(hi, sec, secOff+w*j, false)
			x.vxor3(a, t, hi)
			x.vmul64(a, a, x.kprime)
			continue
		}
		x.vxor(a, a, t)
		x.seedKey(t, sec, secOff+w*j, false)
		x.vxor(t, t, a)
		x.hi32(hi, t)
		x.vmul32(lo, t, x.kprime)
		x.vmul32(hi, hi, x.kprime)
		x.vshl(hi, hi, 32)
		x.vadd(a, lo, hi)
	}
}

// StoreMergeKeysSeeded writes the accumulators xored with each merge's key:
// a run of derived words shifted down by the key's byte offset within its
// first word, or'ed with the run one word on shifted up by the rest. The
// stores go out in the same 16-byte pieces StoreAcc uses, for the same
// reason: the Go merge reads them eight bytes at a time.
func (x *x86) StoreMergeKeysSeeded(p, sec GPR) {
	w := x.mode / 8
	for half, key := range []struct {
		word int
		sh   uint
	}{{seededMergeLoWord, seededMergeLoShift}, {seededMergeHiWord, seededMergeHiShift}} {
		for j := 0; j < x.nvec; j++ {
			lo, hi := x.tmp[0], x.tmp[1]
			first := key.word + x.lanes*j
			x.seedKey(lo, sec, secretConsumeRate*first, first%2 == 1)
			x.seedKey(hi, sec, secretConsumeRate*(first+1), (first+1)%2 == 1)
			x.vshr(lo, lo, key.sh)
			x.vshl(hi, hi, 64-key.sh)
			x.vor(lo, lo, hi)
			x.vxor(lo, lo, x.accA[j])
			x.vstorePieces(lo, p, 8*accNB*half+w*j)
		}
	}
}
