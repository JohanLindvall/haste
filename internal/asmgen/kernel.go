package asmgen

// The kernels. This is the only description of the long-input loop's shape,
// shared by all five backends: the block/scramble structure and the stripe
// counting are written once here, and each Arch supplies the vector steps.
//
// The three entry points mirror dispatch.go in the parent package:
//
//	hashLong<B>(acc *[8]uint64, in unsafe.Pointer, n int, sec unsafe.Pointer, secretLimit int)
//	accum<B>(acc *[8]uint64, in unsafe.Pointer, nbStripes int, sec unsafe.Pointer)
//	scramble<B>(acc *[8]uint64, sec unsafe.Pointer)

// Funcs returns the three function definitions a backend generates, in the
// order EmitAll emits them.
func Funcs(suffix string) []FuncDef {
	return []FuncDef{
		{
			Name: "hashLong" + suffix,
			Args: []string{"acc", "in", "n", "sec", "secretLimit"},
			Doc:  "consumes a whole long input: blocks, scrambles, trailing stripes and the overlapping final stripe",
		},
		{
			Name: "accum" + suffix,
			Args: []string{"acc", "in", "nbStripes", "sec"},
			Doc:  "absorbs nbStripes consecutive stripes, for the streaming path",
		},
		{
			Name: "scramble" + suffix,
			Args: []string{"acc", "sec"},
			Doc:  "applies the between-blocks accumulator scramble",
		},
	}
}

// EmitAll emits the three kernels into one instruction stream per function.
func EmitAll(new func() Arch) []Arch {
	return []Arch{
		emit(new(), emitHashLong),
		emit(new(), emitAccum),
		emit(new(), emitScramble),
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
func emitHashLong(a Arch) {
	b := a.Build()
	acc, in, n, sec, lim := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3), a.ArgGPR(4)
	blk, rem, cnt, s, end, tmp := a.TmpGPR(0), a.TmpGPR(1), a.TmpGPR(2), a.TmpGPR(3), a.TmpGPR(4), a.TmpGPR(5)

	a.Setup()
	a.LoadAcc(acc)

	// end = in + n - 64, the address of the final stripe.
	a.MovRR(end, in)
	a.AddRR(end, n)
	a.SubRI(end, stripeLen)

	// blk = (secretLimit / 8) * 64, the block length in bytes.
	a.MovRR(blk, lim)
	a.ShrRI(blk, 3)
	a.ShlRI(blk, 6)

	a.MovRR(rem, n)
	a.SubRI(rem, 1)

	blockLoop, afterBlocks := b.NewLabel("block"), b.NewLabel("tail")
	b.Label(blockLoop)
	a.BranchR(rem, blk, LT, afterBlocks)
	{
		a.MovRR(cnt, lim)
		a.ShrRI(cnt, 3)
		a.MovRR(s, sec)
		emitStripeLoop(a, in, s, cnt)

		a.Materialize()
		a.MovRR(tmp, sec)
		a.AddRR(tmp, lim)
		a.Scramble(tmp, 0)

		a.SubRR(rem, blk)
		a.Jmp(blockLoop)
	}
	b.Label(afterBlocks)

	// The stripes after the last whole block reuse the secret from its start.
	a.MovRR(cnt, rem)
	a.ShrRI(cnt, 6)
	a.MovRR(s, sec)
	emitStripeLoop(a, in, s, cnt)

	// The final stripe is taken from the end of the input, overlapping
	// whatever came before it, under a secret deliberately misaligned from the
	// per-stripe schedule.
	a.MovRR(tmp, sec)
	a.AddRR(tmp, lim)
	a.SubRI(tmp, secretLastAccStart)
	a.Stripe(0, end, 0, tmp, 0)

	a.Materialize()
	a.StoreAcc(acc)
	a.Finish()
}

// emitAccum is the streaming path's inner step: a run of stripes with no block
// handling, since the caller stops it at each boundary.
func emitAccum(a Arch) {
	acc, in, cnt, sec := a.ArgGPR(0), a.ArgGPR(1), a.ArgGPR(2), a.ArgGPR(3)
	s := a.TmpGPR(0)

	a.Setup()
	a.LoadAcc(acc)
	a.MovRR(s, sec)
	emitStripeLoop(a, in, s, cnt)
	a.Materialize()
	a.StoreAcc(acc)
	a.Finish()
}

func emitScramble(a Arch) {
	acc, sec := a.ArgGPR(0), a.ArgGPR(1)
	a.Setup()
	a.LoadAcc(acc)
	a.Scramble(sec, 0)
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

	a.BranchI(cnt, int64(u), LT, single)
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
	a.SubRI(cnt, int64(u))
	a.BranchI(cnt, int64(u), GE, unrolled)

	b.Label(single)
	a.BranchI(cnt, 0, LE, done)
	loop := b.NewLabel("onebody")
	b.Label(loop)
	a.Stripe(0, in, 0, s, 0)
	a.AddRI(in, stripeLen)
	a.AddRI(s, secretConsumeRate)
	a.SubRI(cnt, 1)
	a.BranchI(cnt, 0, GT, loop)
	b.Label(done)
}

// Constants shared with the parent package. They are wire format: the hash
// changes if any of them does.
const (
	stripeLen          = 64
	secretConsumeRate  = 8
	secretLastAccStart = 7
)
