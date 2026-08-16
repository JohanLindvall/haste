package asmgen

// The XXH64 kernels. XXH64 is a scalar hash -- four 64-bit lanes, each a
// multiply, a rotate and a multiply per eight bytes -- so its kernels are
// integer code, and this file plays the part kernel.go plays for XXH3: the
// shape of the hash written once, against a small surface each architecture
// fills in with its own instructions.
//
// Two kernels per backend, mirroring the parent package's split between a
// one-shot call and a streaming one:
//
//	sum64<B>(in, n, seed) uint64     the whole hash, of any length
//	blocks<B>(lanes, in, nbBlocks)   the lane loop only, for Digest
//
// The one-shot kernel takes every length, short ones included: a short hash
// is nothing but the tail the kernel has anyway, and letting Go handle it
// would put a second call between the caller and the work, which at these
// sizes is a large share of the cost.

// XXH64Funcs returns the two function definitions a scalar backend generates,
// in the order EmitXXH64 emits them. A backend with two lane-round forms
// reads the form from the sixth table slot: nonzero for the split
// multiply and add, zero for the fused one. The choice travels as an argument
// rather than as two kernels because the caller is an inlined wrapper that
// must reach the kernel in one direct call, and a wrapper choosing between
// two callees is past the inliner's budget while a call through a variable
// measured two cycles on an M2 -- a fifth of a short hash.
func XXH64Funcs(suffix string, dual bool) []FuncDef {
	// A dual backend used to take its lane-round form as a fourth argument,
	// which made every caller load a global and every short hash carry a value
	// it never reads. The form now lives in the sixth slot of the primes
	// table, which the kernel holds a pointer to anyway, so only the paths
	// that run the lane loop pay one load for it.
	sumArgs, blockArgs := []string{"in", "n", "seed"}, []string{"lanes", "in", "nbBlocks"}
	_ = dual
	return []FuncDef{
		{
			Name:  "sum64" + suffix,
			Args:  sumArgs,
			Ret:   "uint64",
			Table: "primes",
			Doc:   "hashes the n bytes at in under seed, whatever n is: lanes, merge, tail and avalanche in one call",
		},
		{
			Name:  "blocks" + suffix,
			Args:  blockArgs,
			Table: "primes",
			Doc:   "absorbs nbBlocks whole blocks at in into the four lanes",
		},
	}
}

// XXH64Arch is the surface the XXH64 kernels are written against. The
// register plan is the architecture's: it names the hash, the lanes, the
// loaded words, the primes and a scratch register, and the skeleton only
// moves values between those. Every operation here is one instruction on
// both architectures unless its comment says otherwise.
type XXH64Arch interface {
	Kernel
	Name() string

	// Register plan. H is the running hash; V(i) lane i; X a scratch
	// register for a loaded word; Tmp a scratch register, which may be X
	// itself -- the skeleton never needs both at once. The primes are the
	// architecture's business: the skeleton names them by index, 0..4 for
	// P1..P5, and each backend keeps them where it likes -- all in registers
	// on arm64; on x86, which has no register to spare and no appetite for
	// 64-bit immediates, the two the lane loop uses in registers and the
	// rest as memory operands off the table.
	H() GPR
	V(i int) GPR
	X() GPR
	Tmp() GPR

	// LoadPrimes brings the primes to wherever this backend keeps them,
	// from the table at TableGPR.
	LoadPrimes()
	// AddPrime is dst += Pn; MulPrime is dst *= Pn; MulAddPrime is
	// dst = dst*Pmul + Padd, one instruction on arm64 and two on x86.
	AddPrime(dst GPR, n int)
	MulPrime(dst GPR, n int)
	MulAddPrime(dst GPR, mul, add int)

	// LoadSplit reads the lane-round form -- the sixth table slot -- into
	// dst. Only a Dual backend's skeleton calls it.
	LoadSplit(dst GPR)

	// Dual reports whether the backend has two lane-round forms, selected by
	// the table's form slot; see XXH64Funcs.
	Dual() bool

	// Block absorbs the block at in+off into the four lanes: for each lane,
	// v = rol(v + word*P2, 31) * P1. How the loads and the rounds interleave
	// is the architecture's -- x86 loads each word into X as it goes, arm64
	// pairs the loads -- and so is the round's shape: split says whether the
	// multiply and the add are separate instructions, on a backend that has
	// the choice.
	Block(in GPR, off int, v [4]GPR, split bool)

	// Loads are zero-extending; the offset is an immediate.
	Load64(dst, base GPR, off int)
	Load32(dst, base GPR, off int)
	Load8(dst, base GPR, off int)
	// LoadPair loads two consecutive words: one instruction on arm64, two on
	// x86.
	LoadPair(d0, d1, base GPR, off int)
	// The Adv forms load at base and then advance base by inc: one
	// instruction on arm64 (post-indexed), two on x86. They are what the tail
	// walks with.
	Load64Adv(dst, base GPR, inc int)
	Load32Adv(dst, base GPR, inc int)
	Load8Adv(dst, base GPR, inc int)
	Store64(src, base GPR, off int)

	Mov(dst, src GPR)
	Add(dst, src GPR)
	AddImm(dst GPR, imm int64)
	Sub(dst, src GPR)
	Xor(dst, src GPR)
	// Rol rotates left by an immediate; Rol3 rotates src into dst, one
	// instruction on arm64 and two on x86.
	Rol(dst GPR, sh uint)
	Rol3(dst, src GPR, sh uint)
	// InitLanes sets v1 = seed + P1 + P2, v2 = seed + P2, v3 = seed and
	// v4 = seed - P1: five instructions with three-operand adds, eight
	// without.
	InitLanes(seed GPR, v [4]GPR)
	Shr(dst GPR, sh uint)
	// XorShr is dst ^= dst >> sh: one instruction on arm64, three on x86,
	// which needs Tmp.
	XorShr(dst GPR, sh uint)

	// Round0 is a lane's step with a zero accumulator, in place:
	// x = rol(x*P2, 31) * P1.
	Round0(x GPR)

	// Control flow, shared with the vector backends' surface.
	BranchI(a GPR, imm int64, c Cond, label string)
	SubBranch(r GPR, imm int64, c Cond, label string)
	Jmp(label string)
	// BranchBitClear branches if bit b of r is clear: one instruction on
	// arm64 (tbz), two on x86.
	BranchBitClear(r GPR, b uint, label string)
	// BranchMaskClear branches if r & mask is zero: test and jz on x86, tst
	// and b.eq on arm64.
	BranchMaskClear(r GPR, mask int64, label string)
	// BranchZero branches if r is zero: cbz on arm64, test and jz on x86.
	BranchZero(r GPR, label string)

	// TailMaskSkips reports whether emitTail prefixes the per-bit guards
	// with combined-mask skips. On for x86, where the win was measured; off
	// for arm64, whose short hashes were already level with hand-written
	// assembly on an M2, and whose kernel stays byte-identical until the
	// skips are measured on one.
	TailMaskSkips() bool
}

// EmitXXH64 emits the two kernels into one instruction stream per function.
func EmitXXH64(new func() XXH64Arch) []Kernel {
	a1, a2 := new(), new()
	emitSum64(a1)
	emitBlocks(a2)
	return []Kernel{a1, a2}
}

// emitSum64 is the whole hash of an input of any length.
func emitSum64(a XXH64Arch) {
	b := a.Build()
	in, n, seed := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2)
	h := a.H()
	v := [4]GPR{a.V(0), a.V(1), a.V(2), a.V(3)}
	short, tail := b.NewLabel("short"), b.NewLabel("tail")

	a.LoadPrimes()
	a.BranchI(n, 32, LT, short)
	a.InitLanes(seed, v)

	// The seed is dead now; its register counts the blocks.
	nb := seed
	a.Mov(nb, n)
	a.Shr(nb, 5)
	emitBlockLoops(a, in, nb, v)

	// h = rol1(v1) + rol7(v2) + rol12(v3) + rol18(v4), summed as a tree,
	// then each lane folded in.
	t := a.Tmp()
	a.Rol3(h, v[0], 1)
	a.Rol3(t, v[1], 7)
	a.Add(h, t)
	a.Rol3(t, v[2], 12)
	a.Rol3(nb, v[3], 18)
	a.Add(t, nb)
	a.Add(h, t)
	for i := 0; i < 4; i++ {
		// h = (h ^ round0(v)) * P1 + P4
		a.Round0(v[i])
		a.Xor(h, v[i])
		a.MulAddPrime(h, 0, 3)
	}
	a.Jmp(tail)

	// Under a block there are no lanes: the hash starts from the seed and
	// the fifth prime and is all tail.
	b.Label(short)
	a.Mov(h, seed)
	a.AddPrime(h, 4)

	b.Label(tail)
	a.Add(h, n)
	emitTail(a, h, in, n)
	emitAvalanche(a, h)
}

// emitBlocks is the streaming kernel: the lanes come from and go back to
// memory, and nothing else happens.
func emitBlocks(a XXH64Arch) {
	lanes, in, nb := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2)
	v := [4]GPR{a.V(0), a.V(1), a.V(2), a.V(3)}
	a.LoadPrimes()
	a.LoadPair(v[0], v[1], lanes, 0)
	a.LoadPair(v[2], v[3], lanes, 16)
	emitBlockLoops(a, in, nb, v)
	for i := 0; i < 4; i++ {
		a.Store64(v[i], lanes, 8*i)
	}
}

// emitBlockLoops emits the lane loop, or on a dual backend both forms of
// it, the fused one reached by a branch on the table's form slot.
func emitBlockLoops(a XXH64Arch, in, nb GPR, v [4]GPR) {
	if !a.Dual() {
		emitBlockLoop(a, in, nb, v, false)
		return
	}
	b := a.Build()
	fused, join := b.NewLabel("fused"), b.NewLabel("join")
	a.LoadSplit(a.X())
	a.BranchZero(a.X(), fused)
	emitBlockLoop(a, in, nb, v, true)
	a.Jmp(join)
	b.Label(fused)
	emitBlockLoop(a, in, nb, v, false)
	b.Label(join)
}

// emitBlockLoop absorbs nb blocks from in, advancing in past them. It runs
// two blocks per iteration, with the odd block first: the loop is bound by
// each lane's dependency chain, not by its own overhead, but the cores that
// issue it fastest are the ones the two instructions of overhead per block
// still show on.
func emitBlockLoop(a XXH64Arch, in, nb GPR, v [4]GPR, split bool) {
	b := a.Build()
	even, loop, done := b.NewLabel("even"), b.NewLabel("blocks"), b.NewLabel("bdone")
	a.BranchBitClear(nb, 0, even)
	a.Block(in, 0, v, split)
	a.AddImm(in, 32)
	b.Label(even)
	a.Shr(nb, 1)
	a.BranchZero(nb, done)
	b.Label(loop)
	a.Block(in, 0, v, split)
	a.Block(in, 32, v, split)
	a.AddImm(in, 64)
	a.SubBranch(nb, 1, NE, loop)
	b.Label(done)
}

// emitTail absorbs the n & 31 bytes at p into h: the reference's loops over
// eight-byte words, a four-byte word and single bytes, each guarded by the
// bit of n that says whether it runs. p is left wherever the last read was.
func emitTail(a XXH64Arch, h, p, n GPR) {
	b := a.Build()
	x := a.X()
	step8 := func() {
		// h = rol(h ^ round0(word), 27) * P1 + P4
		a.Load64Adv(x, p, 8)
		a.Round0(x)
		a.Xor(h, x)
		a.Rol(h, 27)
		a.MulAddPrime(h, 0, 3)
	}
	step1 := func() {
		// h = rol(h ^ byte*P5, 11) * P1
		a.Load8Adv(x, p, 1)
		a.MulPrime(x, 4)
		a.Xor(h, x)
		a.Rol(h, 11)
		a.MulPrime(h, 0)
	}
	t8, t4, t2, t1, fin := b.NewLabel("t8"), b.NewLabel("t4"), b.NewLabel("t2"), b.NewLabel("t1"), b.NewLabel("fin")

	// The combined-mask skips ahead of the per-bit guards, where the backend
	// wants them. A trivial tail then pays one or two taken branches instead
	// of up to five: a stripe-multiple n goes straight to the finish, a tail
	// under eight bytes straight to the word-or-less steps, and a tail of
	// whole words straight out. Each skip costs the lengths that do run the
	// steps one not-taken test, which fuses with its jump. Worth 2-4 cycles
	// of a 4..16-byte hash on a Zen 4, where five taken branches in a dozen
	// instructions were most of the gap to cespare/xxhash.
	// A tail that skipped the word steps lands past the whole-words test
	// too: its n & 7 is all of n, and the n == 0 case already left through
	// the first skip.
	t4w := t4
	if a.TailMaskSkips() {
		t4w = b.NewLabel("t4w")
		a.BranchMaskClear(n, 31, fin)
		a.BranchMaskClear(n, 24, t4w)
	}
	a.BranchBitClear(n, 4, t8)
	step8()
	step8()
	b.Label(t8)
	a.BranchBitClear(n, 3, t4)
	step8()
	b.Label(t4)
	if a.TailMaskSkips() {
		a.BranchMaskClear(n, 7, fin)
		b.Label(t4w)
	}
	a.BranchBitClear(n, 2, t2)
	// h = rol(h ^ word32*P1, 23) * P2 + P3
	a.Load32Adv(x, p, 4)
	a.MulPrime(x, 0)
	a.Xor(h, x)
	a.Rol(h, 23)
	a.MulAddPrime(h, 1, 2)
	b.Label(t2)
	if a.TailMaskSkips() {
		a.BranchMaskClear(n, 3, fin)
	}
	a.BranchBitClear(n, 1, t1)
	step1()
	step1()
	b.Label(t1)
	a.BranchBitClear(n, 0, fin)
	step1()
	b.Label(fin)
}

// emitAvalanche is the final mix.
func emitAvalanche(a XXH64Arch, h GPR) {
	a.XorShr(h, 33)
	a.MulPrime(h, 1)
	a.XorShr(h, 29)
	a.MulPrime(h, 2)
	a.XorShr(h, 32)
}
