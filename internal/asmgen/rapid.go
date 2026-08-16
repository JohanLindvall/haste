package asmgen

// The rapidhash kernel, described once for both architectures.
//
// rapidhash has one mixing primitive and no others: the low and high halves
// of a 64x64 multiply, xored together. Everything below is that operation
// against loads and a secret word, so the surface a backend implements is
// small -- narrower than the XXH64 one, which needs rotates, five primes and
// a lane round. What differs between the two architectures is not the shape
// of the work but where the multiply may put its result: arm64's mul and
// umulh are three-operand and unconstrained, while x86's mulq pins RDX:RAX,
// which is why Round is a backend's method rather than three calls from here.
//
// The structure is three length classes that share only their ending:
//
//	0..16    reads its bytes directly, no multiply until the fold
//	17..112  a ladder of up to six rounds, each guarded by the length
//	113+     seven independent lanes over 224-byte blocks, then the ladder
//
// All three converge on a and b, then the same final fold.

// RapidArch is the surface a rapidhash backend implements.
type RapidArch interface {
	Kernel
	Name() string

	// Register plan. Seed is the running hash and lane 0; See(1..6) are the
	// other six lanes, live only above 112 bytes. A and B are the two words
	// the fold consumes, and double as scratch elsewhere. In holds the input
	// pointer and I the length that remains -- the reference's i, which the
	// block loop decrements and the fold keys itself with.
	Seed() GPR
	See(i int) GPR
	A() GPR
	B() GPR
	In() GPR
	I() GPR

	// SeedMix is the prologue's seed ^= mix(seed ^ secret[2], secret[1]).
	SeedMix()

	// SeedConst loads what SeedMix computes when the seed is zero. It lives
	// in the secret table's ninth word so that no kernel has to carry a
	// 64-bit immediate; the table is the one thing both kernels already
	// reach. TestSimulatedBackends runs the unseeded kernel against the
	// portable path, so the two cannot disagree about its value.
	SeedConst()

	// Round is the whole of the hash's work:
	//
	//	lane = mix(load(In+off) ^ secret[slot], load(In+off+8) ^ lane)
	//
	// The two loads are adjacent, which arm64 takes as one instruction and
	// x86 as two; that choice is the backend's.
	Round(lane GPR, off, slot int)

	// Short4to16 fills A and B for a 4..16-byte input and folds the length
	// into the seed: the two reads overlap below 16 bytes, and are 32-bit
	// below 8.
	Short4to16()
	// Short1to3 fills A and B for a 1..3-byte input:
	// a = p[0]<<45 | p[n-1], b = p[n>>1].
	Short1to3()
	// Tail16 sets a = load(In + I - 16) ^ I and b = load(In + I - 8).
	Tail16()

	// Converge folds the seven lanes into Seed, in the reference's fixed
	// tree rather than a chain.
	Converge()
	// SpreadLanes copies Seed into See(1..6) before the block loop.
	SpreadLanes()

	// Finalize computes the returned value into RetGPR:
	//	a ^= secret[1]; b ^= seed; a, b = mum(a, b)
	//	return mix(a ^ secret[7], b ^ secret[1] ^ i)
	Finalize()

	// Zero sets a register to zero; the empty input needs a and b zeroed.
	Zero(dst GPR)

	// AdvanceIn adds bytes to In, and SubI subtracts them from I.
	AdvanceIn(bytes int)
	SubI(bytes int)

	BranchI(a GPR, imm int64, c Cond, label string)
	Jmp(label string)
}

// RapidFuncs is the one function a rapidhash backend generates.
func RapidFuncs(suffix string) []FuncDef {
	return []FuncDef{{
		Name:  "sum64" + suffix,
		Args:  []string{"in", "n", "seed"},
		Ret:   "uint64",
		Table: "secret",
		Doc:   "hashes the n bytes at in under seed, whatever n is: every length class and the fold in one call",
	}, {
		Name:  "sum64" + suffix + "NS",
		Args:  []string{"in", "n"},
		Ret:   "uint64",
		Table: "secret",
		Doc:   "is sum64" + suffix + " with no seed: the prologue's mix is a constant, loaded rather than computed",
	}}
}

// EmitRapid emits the kernel.
func EmitRapid(new func() RapidArch) []Kernel {
	a, ns := new(), new()
	emitRapidSum64(a, true)
	emitRapidSum64(ns, false)
	return []Kernel{a, ns}
}

// ladder is the 17..112 tail: each rung runs only if more than `above` bytes
// remain, and is keyed by `slot`. The reference nests these as six ifs, one
// inside the last; flattened, every rung falls out to the same place, which
// is the same thing and one label instead of six.
//
// The slots run 2,2,1,1,2,1. That is not a pattern, just what the reference
// does -- and wire format either way.
var ladder = []struct{ above, off, slot int }{
	{16, 0, 2}, {32, 16, 2}, {48, 32, 1}, {64, 48, 1}, {80, 64, 2}, {96, 80, 1},
}

// emitRapidSum64 emits the kernel. With seeded false, the prologue's
// seed ^= mix(seed ^ secret[2], secret[1]) is folded to the value it always
// takes when the seed is zero, which the secret table carries as a ninth
// word. That removes a multiply -- a serial one, at the head of every hash,
// before any input has been read -- from every call through Sum64 and
// Sum64String. On a Zen 4 the portable form of the same change measures 46%
// faster at 4 bytes, 41% at 17..32, 32% at 64 and 14% at 128.
func emitRapidSum64(a RapidArch, seeded bool) {
	b := a.Build()
	n := a.I() // the length argument is i from the first instruction
	short, short1to3, empty := b.NewLabel("short"), b.NewLabel("short1to3"), b.NewLabel("empty")
	tail, done := b.NewLabel("tail"), b.NewLabel("done")
	blocks := b.NewLabel("blocks")

	if seeded {
		a.SeedMix()
	} else {
		a.SeedConst()
	}

	// 0..16 is the common case for a hash table key, so it is the branch
	// that falls through rather than the one that jumps.
	a.BranchI(n, 16, GT, blocks)

	// ---- 0..16 -------------------------------------------------------
	b.Label(short)
	a.BranchI(n, 4, LT, short1to3)
	a.Short4to16()
	a.Jmp(done)

	b.Label(short1to3)
	a.BranchI(n, 0, EQ, empty)
	a.Short1to3()
	a.Jmp(done)

	b.Label(empty)
	a.Zero(a.A())
	a.Zero(a.B())
	a.Jmp(done)

	// ---- 17 and up ---------------------------------------------------
	b.Label(blocks)
	a.BranchI(n, 112, LE, tail)

	// lane names the register a block-loop round accumulates into: lane 0 is
	// the seed itself, and each lane is keyed by the secret word of its own
	// index.
	lane := func(i int) GPR {
		if i == 0 {
			return a.Seed()
		}
		return a.See(i)
	}

	// Seven lanes, two groups of seven rounds per iteration. The loop runs
	// while more than 224 bytes remain, then one group while more than 112
	// do. That is the reference's shape, and what guarantees the ladder
	// below always has something left to read: it exits with i > 16.
	a.SpreadLanes()
	loop, one, after := b.NewLabel("loop"), b.NewLabel("one"), b.NewLabel("after")

	a.BranchI(a.I(), 224, LE, one)
	b.Label(loop)
	for group := 0; group < 2; group++ {
		for i := 0; i < 7; i++ {
			a.Round(lane(i), group*112+i*16, i)
		}
	}
	a.AdvanceIn(224)
	a.SubI(224)
	a.BranchI(a.I(), 224, GT, loop)

	b.Label(one)
	a.BranchI(a.I(), 112, LE, after)
	for i := 0; i < 7; i++ {
		a.Round(lane(i), i*16, i)
	}
	a.AdvanceIn(112)
	a.SubI(112)

	b.Label(after)
	a.Converge()

	// ---- 17..112, and whatever the block loop left --------------------
	b.Label(tail)
	last := b.NewLabel("tail16")
	for _, r := range ladder {
		a.BranchI(a.I(), int64(r.above), LE, last)
		a.Round(a.Seed(), r.off, r.slot)
	}
	b.Label(last)
	a.Tail16()

	b.Label(done)
	a.Finalize()
}
