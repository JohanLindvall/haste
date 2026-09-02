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
	b := a.Build()
	acc, in, n, sec, lim := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3), a.ArgGPR(4)
	blk, rem, cnt, s, end, tmp := a.TmpGPR(0), a.TmpGPR(1), a.TmpGPR(2), a.TmpGPR(3), a.TmpGPR(4), a.TmpGPR(5)
	noOverlap(a, 5, 6)

	a.Setup(true)
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
	b.Label(afterBlocks)

	// The stripes after the last whole block reuse the secret from its start.
	a.ShrRRI(cnt, rem, 6)
	a.MovRR(s, sec)
	emitStripeLoop(a, in, s, cnt)

	// The final stripe is taken from the end of the input, overlapping
	// whatever came before it, under a secret deliberately misaligned from the
	// per-stripe schedule.
	a.AddRRR(tmp, sec, lim)
	a.SubRI(tmp, secretLastAccStart)
	a.Stripe(Standalone, end, 0, tmp, 0)

	a.Materialize(true)
	a.StoreAcc(acc)
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
func emitStripeLoop(a Arch, in, s, cnt GPR) {
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
	stripeLen          = 64
	secretConsumeRate  = 8
	secretLastAccStart = 7

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
)
