package asmgen

import "fmt"

// The kernels. This is the only description of the long-input loop's shape,
// shared by all five backends: the block/scramble structure and the stripe
// counting are written once here, and each Arch supplies the vector steps.
//
// The four entry points mirror dispatch.go in the parent package:
//
//	hashLong<B>(acc, in, n, sec, secretLimit)          one-shot, whole input
//	accumBlocks<B>(acc, in, nbStripes, sec, secretLimit, soFar)   streaming
//	accum<B>(acc, in, nbStripes, sec)                  one run, no scramble
//	accumBlocks2<B>(acc, in, nbStripes, sec, secretLimit, soFar, in2, nbStripes2)
//	                                                   streaming, two runs
//
// A SeededArch backend (x86) adds hashLongSeed, and a MergeArch one (arm64)
// the one-shot kernels that finish the hash themselves: hashLong64,
// hashLong128 and, for a DerivedSeedArch, hashLongSeed64 and hashLongSeed128.

// Funcs returns the four function definitions a backend generates, in the
// order EmitAll emits them.
func Funcs(suffix string) []FuncDef {
	return []FuncDef{
		{
			Name: "hashLong" + suffix,
			Args: []string{"acc", "in", "n", "sec", "secretLimit"},
			// The initial accumulators come from the table, not from acc,
			// which is written and never read: every one-shot hash starts
			// from initAcc, and loading it from the global spares the caller
			// a 64-byte copy and the kernel a load that waits on it.
			Table: "initAcc",
			Doc:   "consumes a whole long input -- blocks, scrambles, trailing stripes and the overlapping final stripe -- into acc, starting from initAcc",
		},
		{
			Name: "accumBlocks" + suffix,
			Args: []string{"acc", "in", "nbStripes", "sec", "secretLimit", "soFar"},
			Doc:  "absorbs nbStripes stripes starting soFar stripes into the current block, scrambling at every block boundary it crosses",
		},
		{
			Name: "accum" + suffix,
			Args: []string{"acc", "in", "nbStripes", "sec"},
			Doc:  "absorbs nbStripes consecutive stripes against one secret position, with no scramble",
		},
		{
			Name: "accumBlocks2" + suffix,
			Args: []string{"acc", "in", "nbStripes", "sec", "secretLimit", "soFar", "in2", "nbStripes2"},
			Doc:  "is accumBlocks over two runs, nbStripes stripes at in and then nbStripes2 at in2, as one walk of the block",
		},
	}
}

// EmitAll emits the four kernels into one instruction stream per function.
func EmitAll(new func() Arch) []Arch {
	return []Arch{
		emit(new(), emitHashLong),
		emit(new(), emitAccumBlocks),
		emit(new(), emitAccum),
		emit(new(), emitAccumBlocks2),
	}
}

// SeededFunc is the fifth kernel, which only a SeededArch backend has.
func SeededFunc(suffix string) FuncDef {
	return FuncDef{
		Name: "hashLongSeed" + suffix,
		Args: []string{"keys", "in", "n", "seed"},
		// The accumulators start from initAcc, as in hashLong, and the
		// secret is the default one, whose address the prologue puts in
		// SecretGPR: the seed is applied to it in registers.
		Table:  "initAcc",
		Secret: "kSecret",
		Doc:    "is hashLong under the secret a seed derives from the default one, applying the seed in registers rather than reading a derived copy; it writes the accumulators already keyed for the two merges, by the derived secret at secretMergeAccsStart into keys[0:8] and at secretDefaultSize-64-secretMergeAccsStart into keys[8:16]",
	}
}

// SeededFuncFor is SeededFunc for the backend a, or false if it has no
// such kernel. A DerivedSeedArch backend has seeded kernels of its own
// shape instead, among LongFuncs.
func SeededFuncFor(a Arch, suffix string) (FuncDef, bool) {
	if _, ok := a.(SeededArch); ok {
		return SeededFunc(suffix), true
	}
	return FuncDef{}, false
}

// EmitSeeded emits the seeded one-shot kernel, if the backend has one.
func EmitSeeded(new func() Arch) (Arch, bool) {
	a := new()
	if _, ok := a.(SeededArch); !ok {
		return nil, false
	}
	return emit(a, emitHashLongSeed), true
}

// DerivedSeedArch is a backend whose seeded kernels derive the secret into
// memory, as the reference does, rather than applying the seed at every
// secret load the way SeededArch does.
//
// The derivation is what made the reference's way slow in Go -- a loop of
// 24 scalar loads, adds and stores, a zeroed 192-byte array and two more
// call levels, some 150 instructions of a 256-byte hash -- and on a Zen 4
// its 8-byte stores also could not be forwarded to the kernel's wide loads.
// Neither holds for a kernel on arm64: twelve 16-byte vector adds derive
// the whole secret into the kernel's own frame, which needs no zeroing, and
// a Neoverse N2 forwards 16-byte stores to the loads that follow them,
// misaligned ones included, at a cost measured at about two cycles. What is
// left is a fixed cost per hash, where the seed applied at every load is a
// cost per stripe: on that core two or three more instructions a stripe,
// 7-8% of a loop bound by how many it dispatches, where the fixed cost is
// a few dozen instructions. So these kernels have no length cap. Their
// merges are MergeArch's, from the derived copy.
type DerivedSeedArch interface {
	Arch
	// SecretGPR is the register the prologue puts the default secret's
	// address in.
	SecretGPR() GPR
	// FrameSecret sets dst to the address of the 192-byte buffer in the
	// kernel's frame, 16-byte aligned.
	FrameSecret(dst GPR)
	// DeriveSecret writes the secret the seed in r derives from the default
	// one at [src] to [dst]: each word plus the seed where its index is even
	// and minus it where it is odd.
	DeriveSecret(dst, src, seed GPR)
}

// seededFrame is the frame a DerivedSeedArch kernel asks for. arm64 puts a
// frame's locals at [RSP+8, RSP+8+size), so the secret goes at RSP+16 to
// be 16-byte aligned, and the frame is that much larger than the secret.
const seededFrame = secretDefaultSize + 16

// SeededArch is a backend that can run the seeded one-shot kernel.
//
// A seeded XXH3 of more than 240 bytes is defined as the unseeded hash under
// a secret derived from the default one: word w of it is kSecret's word w
// plus the seed where w is even and minus it where w is odd. Deriving that
// secret into memory and handing it to hashLong, which is what the reference
// does, puts 24 eight-byte stores in front of a kernel whose first act is to
// load 16, 32 or 64 bytes at a time across them. No such load can be
// forwarded from narrower stores, so each one waits for the stores to reach
// the cache: on a Zen 4 that was 20-30 store-to-load-interlock failures and
// about 30 cycles per call, which with the derivation itself made a seeded
// 241-byte hash cost twice an unseeded one.
//
// Every secret load the kernel makes starts at a whole word, at a
// stripe-dependent word index k, and covers consecutive words, so the
// derived value is the default secret's plus a vector of alternating seed
// and negated seed, whose phase is the parity of k. That is one add per
// secret load, of a pattern built once from the seed, and the final
// stripe's key -- which starts one byte into a word -- is the two derived
// vectors either side of it, shifted together. The secret never exists in
// memory at all.
type SeededArch interface {
	Arch
	// SecretGPR is the register the prologue puts the default secret's
	// address in.
	SecretGPR() GPR
	// SetupSeed builds the seed pattern from the seed in r.
	SetupSeed(r GPR)
	// RefreshSeed rebuilds whatever of the pattern the fast block loop's
	// secret registers overwrote.
	RefreshSeed()
	// LoadSecretRegsSeeded is LoadSecretRegs over the seeded secret.
	LoadSecretRegsSeeded(sec GPR)
	// StripeSeeded is Stripe keyed by the seeded secret; odd says the
	// stripe's secret starts at an odd word.
	StripeSeeded(k int, in GPR, inOff int, sec GPR, secOff int, odd bool)
	// FinalStripeSeeded absorbs the final stripe, whose key starts one byte
	// into the odd word at [sec+secOff].
	FinalStripeSeeded(in GPR, inOff int, sec GPR, secOff int)
	// ScrambleSeeded is Scramble keyed by the seeded secret at an even word.
	ScrambleSeeded(sec GPR, secOff int)
	// StoreMergeKeysSeeded stores the materialized accumulators to [p]
	// twice, xored with the seeded secret at the two merges' offsets.
	StoreMergeKeysSeeded(p, sec GPR)
}

// seededSecretLimit is the default secret's secretLimit, the only one the
// seeded kernel handles.
const seededSecretLimit = stdBlockStripes * secretConsumeRate

// emitHashLongSeed is emitHashLong under the seeded default secret; see
// SeededArch. The structure is the same -- blocks, scrambles, trailing
// stripes, the final stripe -- with the secret fixed at the default one's
// shape, which makes the block length and the scramble's offset constants.
func emitHashLongSeed(a Arch) {
	sa := a.(SeededArch)
	b := a.Build()
	acc, in, n, seed := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3)
	// The secret pointer and limit take the registers hashLong has them in,
	// once the seed has been spent on the pattern.
	sec, lim := a.ArgGPR(3), a.ArgGPR(4)
	blk, rem, cnt, s, end, tmp := a.TmpGPR(0), a.TmpGPR(1), a.TmpGPR(2), a.TmpGPR(3), a.TmpGPR(4), a.TmpGPR(5)
	noOverlap(a, 5, 6)
	if sa.SecretGPR() != lim {
		panic("asmgen: the seeded kernel expects its secret where hashLong's limit goes")
	}

	a.Setup(true)
	sa.SetupSeed(seed)
	a.MovRR(sec, sa.SecretGPR())
	a.MovRI(lim, seededSecretLimit)
	a.LoadAcc(a.TableGPR(), true)

	a.AddRRR(end, in, n)
	a.SubRI(end, stripeLen)
	a.MovRI(blk, stdBlockStripes*stripeLen)
	a.SubRRI(rem, n, 1)

	blockLoop, afterBlocks := b.NewLabel("block"), b.NewLabel("tail")
	if ns := a.FastBlockStripes(); ns > 0 {
		if ns != stdBlockStripes {
			panic("asmgen: seeded fast block of an unexpected length")
		}
		fast, generic := b.NewLabel("fast"), b.NewLabel("gen")
		a.BranchI(rem, int64(ns*stripeLen*minFastBlocksSeeded), LT, generic)
		sa.LoadSecretRegsSeeded(sec)
		a.AddRRR(tmp, sec, lim)
		b.Label(fast)
		for k := 0; k < ns; k++ {
			a.FastStripe(k, in, stripeLen*k)
		}
		a.AddRI(in, int64(stripeLen*ns))
		a.Materialize(false)
		sa.ScrambleSeeded(tmp, 0)
		a.SubRR(rem, blk)
		a.BranchR(rem, blk, GE, fast)
		sa.RefreshSeed()
		a.Jmp(afterBlocks)
		b.Label(generic)
	}
	b.Label(blockLoop)
	a.BranchR(rem, blk, LT, afterBlocks)
	{
		a.MovRI(cnt, stdBlockStripes)
		a.MovRR(s, sec)
		emitStripeLoopSeeded(sa, in, s, cnt)

		a.Materialize(false)
		a.AddRRR(tmp, sec, lim)
		sa.ScrambleSeeded(tmp, 0)

		a.SubRR(rem, blk)
		a.Jmp(blockLoop)
	}
	b.Label(afterBlocks)

	a.ShrRRI(cnt, rem, 6)
	a.MovRR(s, sec)
	emitStripeLoopSeeded(sa, in, s, cnt)

	// The final stripe's key starts secretLastAccStart bytes before the
	// limit, which is one byte into the odd word before it.
	sa.FinalStripeSeeded(end, 0, sec, seededSecretLimit-secretConsumeRate)

	// The merges read the secret at unaligned offsets too, and a Go merge
	// assembling those words from the seed took sixteen of them for a
	// 128-bit hash -- more live values than x86 has registers, which
	// spilled. The kernel has the seed pattern in vector registers already,
	// so it keys both merges' inputs itself, and the Go side only folds.
	a.Materialize(true)
	sa.StoreMergeKeysSeeded(acc, sec)
	a.Finish()
}

// Merge-key geometry for the seeded kernel: the 64-bit merge (and the 128-bit
// hash's low half) reads the secret at secretMergeAccsStart, eleven bytes,
// which is word 1 shifted down three bytes; the 128-bit high half reads it at
// the default size less a stripe less that, 117 bytes, which is word 14
// shifted down five.
const (
	seededMergeLoWord  = 1
	seededMergeLoShift = 24
	seededMergeHiWord  = 14
	seededMergeHiShift = 40
)

// emitStripeLoopSeeded is emitStripeLoop for the seeded kernel, which needs
// to know each stripe's parity. Every run starts at an even word -- the
// start of a block or of the tail -- and the groups are an even number of
// stripes, so within a group the parity is the stripe's index; the at most
// three stripes left after the groups are written out rather than looped,
// so theirs is fixed too.
//
// Those last stripes read at fixed offsets and leave in and s where the
// groups left them. Only the tail reaches them -- a block is sixteen
// stripes, a whole number of groups at either unroll -- and the tail's
// pointers are dead afterwards.
func emitStripeLoopSeeded(sa SeededArch, in, s, cnt GPR) {
	a := Arch(sa)
	b := a.Build()
	u := a.Unroll()
	if u%2 != 0 || u > 8 {
		panic("asmgen: seeded stripe loop needs an even unroll of at most eight")
	}
	unrolled, single, done := b.NewLabel("unroll"), b.NewLabel("one"), b.NewLabel("done")

	a.SubBranch(cnt, int64(u), LT, single)
	a.GroupBegin(s)
	b.Label(unrolled)
	for k := 0; k < u; k++ {
		sa.StripeSeeded(k, in, stripeLen*k, s, secretConsumeRate*k, k%2 == 1)
	}
	a.AddRI(in, int64(stripeLen*u))
	a.AddRI(s, int64(secretConsumeRate*u))
	a.SubBranch(cnt, int64(u), GE, unrolled)

	b.Label(single)
	a.AddRI(cnt, int64(u))
	if u >= 8 {
		half := u / 2
		singles := b.NewLabel("singles")
		a.BranchI(cnt, int64(half), LT, singles)
		a.GroupBegin(s)
		for k := 0; k < half; k++ {
			sa.StripeSeeded(k, in, stripeLen*k, s, secretConsumeRate*k, k%2 == 1)
		}
		a.AddRI(in, int64(stripeLen*half))
		a.AddRI(s, int64(secretConsumeRate*half))
		a.SubRI(cnt, int64(half))
		b.Label(singles)
	}
	for i := 0; i < 3; i++ {
		a.BranchI(cnt, int64(i), LE, done)
		sa.StripeSeeded(Standalone, in, stripeLen*i, s, secretConsumeRate*i, i%2 == 1)
	}
	b.Label(done)
}

// noOverlap refuses a kernel whose first ntmp temporaries share a register
// with one of its nargs arguments. The two pools overlap on purpose at the
// far end -- a kernel with few arguments may use the registers the eight-
// argument one takes -- so each emitter states what it uses.
func noOverlap(a Arch, nargs, ntmp int) {
	for i := 0; i < nargs; i++ {
		for j := 0; j < ntmp; j++ {
			if a.ArgGPR(i) == a.TmpGPR(j) {
				panic(fmt.Sprintf("asmgen: %s: temporary %d and argument %d share %s",
					a.Name(), j, i, a.GPRName(a.TmpGPR(j))))
			}
		}
	}
}

func emit(a Arch, f func(Arch)) Arch {
	f(a)
	return a
}

// emitHashLong is the whole long-input path.
//
// The loop is driven by a byte count rather than a block count so that no
// division is needed: the reference's (len-1)/blockLen becomes "while at least
// one block remains". Holding one byte back is what guarantees the final
// stripe always has 64 bytes to read, even when the input ends exactly on a
// stripe boundary.
//
// The accumulators start from the initAcc table, whose address the prologue
// puts in TableGPR, and acc is only written. The copy this replaces was made
// by Go with 16-byte stores that the kernel's loads then had to wait for --
// the reason LoadAcc reads a caller's array in pieces -- where a global that
// was written once at init can be read at full width with nothing to wait on.
// Measured on a Zen 4 within one binary, the copy against no copy: -1.5% at
// 256 bytes, -1.8% at 512, -1.9% at a kibibyte, -0.5% at 4 KiB; with the
// assembly dispatcher that landed beside it, Sum64 is 6-7% quicker over
// 256..1024 bytes and Sum128 4-7%.
func emitHashLong(a Arch) {
	emitHashLongWith(a, nil, func() { a.StoreAcc(a.ArgGPR(0)) })
}

// longArgs are the registers hashLong's body finds its input, length,
// secret and limit in.
type longArgs struct{ in, n, sec, lim GPR }

// MergeArch is a backend whose one-shot kernels finish the hash themselves:
// the accumulators keyed, folded and summed and the result avalanched in
// the kernel, where hashLong stores them for Go to reload and do the same.
//
// What that saves is everything around the merge rather than the merge's
// own arithmetic, which is the same instructions either way: the zeroing of
// the array Go passes for the accumulators (the compiler owes a //go:noescape
// callee that), the kernel's stores into it and Go's loads out of it, and a
// 64-bit constant or two; on arm64 a vector-to-general-purpose move stands in
// for a store and a reload. On a Neoverse N2 that was 22 instructions of a
// 256-byte hash: Sum64 2.7% faster there, Sum128 4.6%, 3.3% and 1.7% at 256,
// 512 and 1024 bytes, and the seeded hashes 7% from 256 bytes to a
// kibibyte. The constants come from the table the kernel already holds for
// initAcc, which is why these kernels read longTable -- initAcc and then the
// avalanche multiplier -- rather than initAcc: an earlier version of this,
// with the constants built by movz and movk, measured neutral.
type MergeArch interface {
	Arch
	// LongStart sets dst to n times the table's word slot, complemented if
	// not: the merge's starting value.
	LongStart(dst, n GPR, slot int, not bool)
	// KeyAt sets dst = src + off.
	KeyAt(dst, src GPR, off int)
	// MergeLong leaves in ret avalanche(start + the four folds of the
	// materialized accumulators, keyed by the eight words at [key]). It
	// leaves the accumulators as they were, and the registers named in keep.
	MergeLong(ret, start, key GPR, keep ...GPR)
	// StorePair stores lo and hi to [p] and [p+8].
	StorePair(lo, hi, p GPR)
}

// The words of longTable the merging kernels read beyond the accumulators'
// start: prime64_1 and prime64_2 are initAcc's second and third, and the
// avalanche multiplier follows initAcc.
const (
	longSlotPrime1    = 1
	longSlotPrime2    = 2
	longSlotAvalanche = 8
)

// LongFuncs are the one-shot kernels of a MergeArch backend that return the
// hash rather than the accumulators, unseeded and, for a DerivedSeedArch,
// seeded.
func LongFuncs(a Arch, suffix string) []FuncDef {
	if _, ok := a.(MergeArch); !ok {
		return nil
	}
	defs := []FuncDef{{
		Name:  "hashLong64" + suffix,
		Args:  []string{"in", "n", "sec", "secretLimit"},
		Ret:   "uint64",
		Table: "longTable",
		Doc:   "is the 64-bit XXH3 of a long input: hashLong, and then the merge and avalanche in the kernel",
	}, {
		Name:  "hashLong128" + suffix,
		Args:  []string{"out", "in", "n", "sec", "secretLimit"},
		Table: "longTable",
		Doc:   "is the 128-bit XXH3 of a long input, low half then high into out: hashLong, and then both merges and avalanches in the kernel",
	}}
	if _, ok := a.(DerivedSeedArch); ok {
		defs = append(defs, FuncDef{
			Name:   "hashLongSeed64" + suffix,
			Args:   []string{"in", "n", "seed"},
			Ret:    "uint64",
			Table:  "longTable",
			Secret: "kSecret",
			Frame:  seededFrame,
			Doc:    "is the 64-bit XXH3 of a long input under seed: the secret derived into the kernel's frame, hashLong over it, and the merge in the kernel",
		}, FuncDef{
			Name:   "hashLongSeed128" + suffix,
			Args:   []string{"out", "in", "n", "seed"},
			Table:  "longTable",
			Secret: "kSecret",
			Frame:  seededFrame,
			Doc:    "is the 128-bit XXH3 of a long input under seed, low half then high into out",
		})
	}
	return defs
}

// EmitLong emits LongFuncs' kernels, in the same order.
func EmitLong(new func() Arch) []Arch {
	if _, ok := new().(MergeArch); !ok {
		return nil
	}
	ks := []Arch{emit(new(), emitHashLong64), emit(new(), emitHashLong128)}
	if _, ok := new().(DerivedSeedArch); ok {
		ks = append(ks, emit(new(), emitHashLongSeed64), emit(new(), emitHashLongSeed128))
	}
	return ks
}

func emitHashLong64(a Arch) {
	args := longArgs{a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3)}
	emitHashLongArgs(a, args, nil, func() { emitMerge64(a, args) })
}

func emitHashLong128(a Arch) {
	args := longArgs{a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3), a.ArgGPR(4)}
	emitHashLongArgs(a, args, nil, func() { emitMerge128(a, args, a.ArgGPR(0)) })
}

// seededLong is the seeded merging kernels' prologue:
// the secret derived into the frame, and sec and lim pointed at it.
func seededLong(a Arch, seed, sec, lim GPR) func() {
	da := a.(DerivedSeedArch)
	if da.SecretGPR() != lim {
		panic("asmgen: the seeded kernel expects its secret where hashLong's limit goes")
	}
	return func() {
		buf := a.TmpGPR(0)
		da.FrameSecret(buf)
		da.DeriveSecret(buf, da.SecretGPR(), seed)
		a.MovRR(sec, buf)
		a.MovRI(lim, seededSecretLimit)
	}
}

func emitHashLongSeed64(a Arch) {
	// The seed arrives where hashLong's secret goes; the derived secret's
	// pointer takes the next register and its limit the one after, which
	// is where the prologue left the default secret.
	args := longArgs{a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(3), a.ArgGPR(4)}
	emitHashLongArgs(a, args, seededLong(a, a.ArgGPR(2), args.sec, args.lim), func() { emitMerge64(a, args) })
}

func emitHashLongSeed128(a Arch) {
	args := longArgs{a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3), a.ArgGPR(4)}
	emitHashLongArgs(a, args, seededLong(a, a.ArgGPR(3), args.sec, args.lim), func() { emitMerge128(a, args, a.ArgGPR(0)) })
}

// emitMerge64 is the 64-bit merge: start from n*prime64_1, keyed by the
// secret at secretMergeAccsStart, into RetGPR.
func emitMerge64(a Arch, args longArgs) {
	ma := a.(MergeArch)
	start, key := a.TmpGPR(0), a.TmpGPR(1)
	ma.LongStart(start, args.n, longSlotPrime1, false)
	ma.KeyAt(key, args.sec, secretMergeAccsStart)
	ma.MergeLong(a.RetGPR(), start, key)
}

// emitMerge128 is both merges: the low half as the 64-bit hash, the high
// half from ^(n*prime64_2) keyed by the secret secretMergeAccsStart before
// its last stripe, and both stored to [out].
func emitMerge128(a Arch, args longArgs, out GPR) {
	ma := a.(MergeArch)
	start, key, lo, hi := a.TmpGPR(0), a.TmpGPR(1), a.TmpGPR(2), a.TmpGPR(3)
	ma.LongStart(start, args.n, longSlotPrime1, false)
	ma.KeyAt(key, args.sec, secretMergeAccsStart)
	ma.MergeLong(lo, start, key, out)
	ma.LongStart(start, args.n, longSlotPrime2, true)
	a.AddRRR(key, args.sec, args.lim)
	a.SubRI(key, secretMergeAccsStart)
	ma.MergeLong(hi, start, key, out, lo)
	ma.StorePair(lo, hi, out)
}

// emitHashLongWith is emitHashLong with two hooks: prologue runs after
// Setup, before the accumulators are loaded, and store replaces the final
// StoreAcc. emitHashLongArgs is the same with the argument registers named:
// the merging kernels take different arguments, and the seeded ones derive
// their secret in the first hook and merge in the second.
func emitHashLongWith(a Arch, prologue, store func()) {
	emitHashLongArgs(a, longArgs{a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3), a.ArgGPR(4)}, prologue, store)
}

func emitHashLongArgs(a Arch, args longArgs, prologue, store func()) {
	b := a.Build()
	in, n, sec, lim := args.in, args.n, args.sec, args.lim
	blk, rem, cnt, s, end, tmp := a.TmpGPR(0), a.TmpGPR(1), a.TmpGPR(2), a.TmpGPR(3), a.TmpGPR(4), a.TmpGPR(5)
	noOverlap(a, 5, 6)

	late, lateSetup := a.(LateScrambleSetup)
	a.Setup(!lateSetup)
	if prologue != nil {
		prologue()
	}
	a.LoadAcc(a.TableGPR(), true)

	// end = in + n - 64, the address of the final stripe.
	a.AddRRR(end, in, n)
	a.SubRI(end, stripeLen)

	// blk = (secretLimit / 8) * 64, the block length in bytes.
	a.ShrRRI(blk, lim, 3)
	a.ShlRI(blk, 6)

	a.SubRRI(rem, n, 1)

	blockLoop, afterBlocks := b.NewLabel("block"), b.NewLabel("tail")
	if ns := a.FastBlockStripes(); ns > 0 {
		// The standard secret's block is a fixed sixteen stripes, which is
		// few enough to hold the whole schedule in registers. Any other
		// secret length drops through to the loop below, and so does an input
		// with too few blocks to pay for filling them.
		fast, generic := b.NewLabel("fast"), b.NewLabel("gen")
		a.BranchI(lim, int64(ns*secretConsumeRate), NE, generic)
		a.BranchI(rem, int64(ns*stripeLen*minFastBlocks), LT, generic)
		a.LoadSecretRegs(sec)
		a.AddRRR(tmp, sec, lim)
		b.Label(fast)
		for k := 0; k < ns; k++ {
			a.FastStripe(k, in, stripeLen*k)
		}
		a.AddRI(in, int64(stripeLen*ns))
		a.Materialize(false)
		a.Scramble(tmp, 0)
		a.SubRR(rem, blk)
		a.BranchR(rem, blk, GE, fast)
		a.Jmp(afterBlocks)
		b.Label(generic)
	}
	if lateSetup {
		// The scramble's constants are built only on the path that reaches
		// a scramble, and the block loop is tested at its foot: an input
		// of a kibibyte or less, which runs no block, skips both.
		if a.FastBlockStripes() > 0 {
			panic("asmgen: a late scramble setup with a fast block loop")
		}
		a.BranchR(rem, blk, LT, afterBlocks)
		late.SetupScramble()
		b.Label(blockLoop)
		a.ShrRRI(cnt, lim, 3)
		a.MovRR(s, sec)
		emitStripeLoop(a, in, s, cnt)

		a.Materialize(false)
		a.AddRRR(tmp, sec, lim)
		a.Scramble(tmp, 0)

		a.SubRR(rem, blk)
		a.BranchR(rem, blk, GE, blockLoop)
	} else {
		b.Label(blockLoop)
		a.BranchR(rem, blk, LT, afterBlocks)
		{
			a.ShrRRI(cnt, lim, 3)
			a.MovRR(s, sec)
			emitStripeLoop(a, in, s, cnt)

			a.Materialize(false)
			a.AddRRR(tmp, sec, lim)
			a.Scramble(tmp, 0)

			a.SubRR(rem, blk)
			a.Jmp(blockLoop)
		}
	}
	b.Label(afterBlocks)

	// The stripes after the last whole block reuse the secret from its start.
	// Nothing reads in or s after them, which lets a backend that wants it
	// take the last few without advancing either.
	a.ShrRRI(cnt, rem, 6)
	a.MovRR(s, sec)
	emitStripeLoopTail(a, in, s, cnt, true)

	// The final stripe is taken from the end of the input, overlapping
	// whatever came before it, under a secret deliberately misaligned from the
	// per-stripe schedule.
	a.AddRRR(tmp, sec, lim)
	a.SubRI(tmp, secretLastAccStart)
	a.Stripe(Standalone, end, 0, tmp, 0)

	a.Materialize(true)
	store()
	a.Finish()
}

// emitAccum is the streaming path's inner step: a run of stripes with no block
// handling, since the caller stops it at each boundary.
func emitAccum(a Arch) {
	acc, in, cnt, sec := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3)
	s := a.TmpGPR(0)
	noOverlap(a, 4, 1)

	// This kernel never scrambles, so it needs no multiplier.
	a.Setup(false)
	a.LoadAcc(acc, false)
	a.MovRR(s, sec)
	emitStripeLoop(a, in, s, cnt)
	a.Materialize(true)
	a.StoreAcc(acc)
	a.Finish()
}

// emitAccumBlocks is the streaming path's outer step. It exists because the
// alternative -- letting Go drive one call per block -- pays to load, fold and
// store the accumulators at every 1 KiB boundary, which measured out at 9% of
// a large single Write and considerably more when the caller writes in small
// pieces.
//
// The caller works out the new position within the block itself: it advances
// by nbStripes and wraps, which needs no return value.
func emitAccumBlocks(a Arch) { emitBlockWalk(a, 1) }

// emitAccumBlocks2 is the same walk over two runs of stripes, the second
// picking up the block position where the first left it, with one prologue
// and epilogue around both. It is for absorb's general path, where the run
// is the stripes staged in the Digest's buffer followed by the ones straight
// out of the caller's slice: the two are never contiguous, and the buffer
// holds one stripe -- the one held back from the previous write in case it
// was the message's last -- whenever writes come in whole kibibytes. Absorbed
// in a call of its own, that stripe cost 123 instructions and 26 cycles on a
// Redwood Cove, for four cycles of work; skipping the call outright, wrong
// results and all, bounded the fusion at 20% of a 1 KiB write and 7% of a
// 4 KiB one there.
//
// It is a second kernel rather than two more arguments on accumBlocks so that
// the drain a small write makes, which has one run, pays nothing for it: the
// seventh argument was tried that way once and cost 64-byte writes 5% on a
// Zen 4.
func emitAccumBlocks2(a Arch) { emitBlockWalk(a, 2) }

func emitBlockWalk(a Arch, runs int) {
	b := a.Build()
	acc, in, left, sec := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3)
	lim, soFar := a.ArgGPR(4), a.ArgGPR(5)
	nspb, s, k, cnt, tmp := a.TmpGPR(0), a.TmpGPR(1), a.TmpGPR(2), a.TmpGPR(3), a.TmpGPR(4)
	var in2, left2 GPR
	if runs == 2 {
		in2, left2 = a.ArgGPR(6), a.ArgGPR(7)
		noOverlap(a, 8, 5)
	} else {
		noOverlap(a, 6, 5)
	}

	a.Setup(true)
	a.LoadAcc(acc, false)

	a.ShrRRI(nspb, lim, 3)

	// The first run is however much of the current block is left; every run
	// after a scramble is a whole block.
	a.AddShl(s, sec, soFar, 3)
	a.SubRRR(k, nspb, soFar)

	// out is where a run ends: the epilogue, or with two runs the switch to
	// the second, which comes back to loop with s and k carried over.
	loop, next, done := b.NewLabel("blocks"), b.NewLabel("bnext"), b.NewLabel("bdone")
	out := done
	if runs == 2 {
		out = next
	}
	b.Label(loop)
	a.BranchI(left, 0, LE, out)
	if ns := a.FastBlockStripes(); ns > 0 {
		// Whole blocks of the standard length run with the secret schedule in
		// registers; see FastStripe. It covers only a position at a block
		// boundary with enough blocks left to pay for filling them, so the
		// registers are filled at most once per call and never for a caller
		// writing a few stripes at a time. Once filled they are free, which is
		// why the loop below carries on at one block rather than four.
		//
		// The block-count test comes first: a caller writing in small pieces
		// fails it on every pass and never reaches the other two.
		fast, slow := b.NewLabel("fast"), b.NewLabel("slow")
		a.BranchI(left, int64(ns*minFastBlocks), LT, slow)
		a.BranchI(lim, int64(ns*secretConsumeRate), NE, slow)
		a.BranchR(k, nspb, NE, slow)
		a.LoadSecretRegs(sec)
		a.AddRRR(tmp, sec, lim)
		b.Label(fast)
		for i := 0; i < ns; i++ {
			a.FastStripe(i, in, stripeLen*i)
		}
		a.AddRI(in, int64(stripeLen*ns))
		a.SubRI(left, int64(ns))
		a.Materialize(false)
		a.Scramble(tmp, 0)
		a.BranchI(left, int64(ns), GE, fast)
		a.Jmp(loop)
		b.Label(slow)
	}
	{
		a.Min(cnt, k, left)

		a.SubRR(left, cnt)
		a.MovRR(tmp, cnt)
		emitStripeLoop(a, in, s, cnt)

		// Anything short of the whole run means the input ran out first;
		// k then holds what is left of the block, and s, which the stripe
		// loop advanced, where in it the next run picks up.
		a.SubRR(k, tmp)
		a.BranchI(k, 0, NE, out)

		a.Materialize(false)
		a.AddRRR(tmp, sec, lim)
		a.Scramble(tmp, 0)

		a.MovRR(s, sec)
		a.MovRR(k, nspb)
		a.Jmp(loop)
	}
	if runs == 2 {
		b.Label(next)
		a.BranchI(left2, 0, LE, done)
		a.MovRR(in, in2)
		a.MovRR(left, left2)
		a.MovRI(left2, 0)
		a.Jmp(loop)
	}
	b.Label(done)

	a.Materialize(true)
	a.StoreAcc(acc)
	a.Finish()
}

// emitStripeLoop runs cnt stripes from in, advancing in by 64 and s by 8 per
// stripe, and leaves both pointers past the last stripe consumed.
//
// It is unrolled because the per-stripe work is only a handful of vector
// operations, so the loop overhead would otherwise be a measurable share of
// it; the remainder loop handles counts that are not a multiple of the unroll,
// which happens on the trailing stripes of every input.
func emitStripeLoop(a Arch, in, s, cnt GPR) { emitStripeLoopTail(a, in, s, cnt, false) }

// LateScrambleSetup is a backend whose hashLong builds the scramble's
// constants only on the path that reaches a scramble, rather than in its
// prologue. Setup(false) then covers whatever every path needs.
type LateScrambleSetup interface {
	// SetupScramble builds what Scramble needs beyond Setup(false).
	SetupScramble()
}

// TailWriter is a backend whose stripe loop, where nothing reads its
// pointers afterwards, takes the at most three stripes the groups leave
// written out at fixed offsets rather than as a loop that advances them:
// a compare and a branch a stripe where the loop spends two adds, a
// subtract and a branch. hashLong's tail is the one such place, and every
// input of 1..1024 bytes past the ladders ends in it.
type TailWriter interface {
	WriteOutTail() bool
}

// emitStripeLoopTail is emitStripeLoop; dead says nothing reads in or s
// after it, which a TailWriter takes up.
func emitStripeLoopTail(a Arch, in, s, cnt GPR, dead bool) {
	b := a.Build()
	u := a.Unroll()
	unrolled, single, done := b.NewLabel("unroll"), b.NewLabel("one"), b.NewLabel("done")

	// The counter runs biased by -u while the unrolled loop is live: it holds
	// the stripes left after the group about to run, and goes negative
	// exactly when a whole group no longer fits. That makes the loop's step
	// and its test one flag-setting subtract instead of a subtract and a
	// compare -- an instruction per group, which matters on cores that issue
	// this loop as fast as they can fetch it.
	a.SubBranch(cnt, int64(u), LT, single)
	a.GroupBegin(s)
	b.Label(unrolled)
	for k := 0; k < u; k++ {
		if a.SecretImm() {
			a.Stripe(k, in, stripeLen*k, s, secretConsumeRate*k)
		} else {
			a.Stripe(k, in, stripeLen*k, s, 0)
			a.AddRI(s, secretConsumeRate)
		}
	}
	a.AddRI(in, int64(stripeLen*u))
	if a.SecretImm() {
		a.AddRI(s, int64(secretConsumeRate*u))
	}
	a.SubBranch(cnt, int64(u), GE, unrolled)

	b.Label(single)
	a.AddRI(cnt, int64(u))
	a.BranchI(cnt, 0, LE, done)
	if u >= 8 {
		// A half-width group between the unrolled loop and the singles.
		// Every power-of-two length leaves fifteen stripes after its last
		// whole block, which at unroll 8 was one group and seven single
		// iterations, each with its own pointer steps and count; this makes
		// it one group, a half group and three. It sits after the zero test
		// so that a run of whole groups -- every block of the block loop --
		// leaves the way it did, with no extra branch taken; the test is a
		// compare rather than the biased subtract above, so that the group
		// path falls through with no jump over a fix-up. Measured on a
		// Redwood Cove, AVX2: 1-2% off a 512-byte hash and off 1 KiB
		// writes streamed, under 1% off a kibibyte, level at 4 KiB and up.
		// With the compare ahead of the zero test instead, 16 KiB paid 0.8%
		// for a taken branch per block.
		half := u / 2
		singles := b.NewLabel("singles")
		a.BranchI(cnt, int64(half), LT, singles)
		a.GroupBegin(s)
		for k := 0; k < half; k++ {
			if a.SecretImm() {
				a.Stripe(k, in, stripeLen*k, s, secretConsumeRate*k)
			} else {
				a.Stripe(k, in, stripeLen*k, s, 0)
				a.AddRI(s, secretConsumeRate)
			}
		}
		a.AddRI(in, int64(stripeLen*half))
		if a.SecretImm() {
			a.AddRI(s, int64(secretConsumeRate*half))
		}
		a.SubRI(cnt, int64(half))
		a.BranchI(cnt, 0, LE, done)
		b.Label(singles)
	}
	if tw, ok := a.(TailWriter); ok && dead && tw.WriteOutTail() && a.SecretImm() {
		// The zero test above has left one to three; the groups leave
		// fewer than four whatever the unroll.
		for i := 0; i < 3; i++ {
			if i > 0 {
				a.BranchI(cnt, int64(i), LE, done)
			}
			a.Stripe(Standalone, in, stripeLen*i, s, secretConsumeRate*i)
		}
		b.Label(done)
		return
	}
	loop := b.NewLabel("onebody")
	b.Label(loop)
	a.Stripe(Standalone, in, 0, s, 0)
	a.AddRI(in, stripeLen)
	a.AddRI(s, secretConsumeRate)
	a.SubBranch(cnt, 1, GT, loop)
	b.Label(done)
}

// Constants shared with the parent package. They are wire format: the hash
// changes if any of them does.
const (
	stripeLen            = 64
	secretConsumeRate    = 8
	secretLastAccStart   = 7
	secretMergeAccsStart = 11

	// secretDefaultSize is the default secret's length, the only one the
	// seeded kernels derive.
	secretDefaultSize = 192

	// stdBlockStripes is the block length the default 192-byte secret gives:
	// (192-64)/8. It is not wire format -- a custom secret of another length
	// produces another block -- but it is the only one worth specializing.
	stdBlockStripes = 16

	// minFastBlocks is how many whole blocks an input needs before filling the
	// secret registers pays for itself. Filling them costs sixteen loads and
	// then holds sixteen registers for the rest of the call, which on a Zen 4
	// is a real share of the rename pool. Measured there, the crossover sits
	// between three blocks and seven: at 2 KiB the register-resident block is
	// 8% slower, at 4 KiB 2% slower, at 8 KiB 2% faster and at 16 KiB 3%.
	minFastBlocks = 4

	// minFastBlocksSeeded is minFastBlocks for the seeded kernel, which pays
	// from fewer blocks: its registers hold keys the generic loop would have
	// to add the seed to, one vpaddq a stripe, where the unseeded kernel's
	// only save a folded load. Measured on a Zen 4 against four: 4.4% faster
	// at 3 KiB and 7.8% at 4 KiB (the 128-bit hash 0.6% and 3.8%), and no
	// different elsewhere. One block is too few: 3-5% slower over
	// 1025..2048 bytes, for the sixteen adds of the fill up front.
	minFastBlocksSeeded = 2
)
