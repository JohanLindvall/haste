# Working in this repository

xxhaste is an XXH3 implementation whose assembly kernels are generated. Read
this before changing anything under `internal/asmgen` or any `.s` file.

## Layout

| path | what it is |
|---|---|
| `xxhaste.go` | public API; `sum64`/`sum128` hold the 0..16-byte cases inline |
| `fixed.go` | call-free entry points for compile-time-known sizes |
| `generic.go` | portable implementation: mid-size ladders, accumulator loop, convergence |
| `digest.go` | streaming `Digest`; same output as `XXH3_update`, different staging |
| `dispatch_{amd64,arm64}.go` | CPU detection and backend selection |
| `dispatch.go` | the same three entry points for purego / other architectures |
| `stub_{amd64,arm64}.go` | **generated** Go declarations for the kernels |
| `xxh_*_{amd64,arm64}.s` | **generated** kernels |
| `internal/asmgen` | the generator |
| `cpu_linux_arm64.go` | SVE2 detection, and the MIDR list gating the hybrid |
| `bench/` | separate module; comparison against zeebo/xxh3 and cespare/xxhash |

`bench/` is its own module on purpose: the library itself must keep importing
nothing outside the standard library.

## The three kernel entry points

Everything architecture-specific is behind exactly these:

```go
hashLong(acc, in, n, sec, secretLimit)   // whole long input: blocks, scrambles, final stripe
accumBlocks(acc, in, nbStripes, sec, secretLimit, soFar)  // streaming: walks block boundaries
accumStripes(acc, in, nbStripes, sec)    // one run, one secret position, no scramble
```

`accumBlocks` exists because driving the block walk from Go costs a load, fold
and store of the accumulators at every 1 KiB boundary: 12% of a large single
Write. It takes the position within the current block and does not report the
new one, because the caller can derive it — advance by nbStripes and wrap.

`secretLimit` is `len(secret) - 64`, **not** `nbStripesPerBlock * 8`. They
differ when the secret length is not a multiple of 8, and the reference uses
the former for both the scramble key and the final stripe's key. Getting this
wrong only shows up with custom secrets of length 137, 193, and so on — which
is why `refSecretVecs` includes them.

## Regenerating assembly

```
go generate ./...                          # everything
go run ./internal/asmgen/gen -only avx512  # one backend
```

Needs binutils for each target being regenerated:

```
apt-get install binutils-x86-64-linux-gnu binutils-aarch64-linux-gnu
```

The generator assembles with the system assembler, disassembles the object, and
writes `BYTE`/`WORD` directives with each instruction's disassembly as a
comment. It requires one emitted instruction to produce exactly one machine
instruction; if you add an emitter that expands to two, `renderBody` fails
loudly rather than silently misplacing labels.

## Testing

```
go test ./...                                        # native, assembly backends
go test -tags purego ./...                           # portable path
go test -race ./...                                  # no shared state after init
GOARCH=amd64 go test -c -o /tmp/x.test . && qemu-x86_64-static -cpu max /tmp/x.test
GOARCH=amd64 go test -c -o /tmp/x.test . && qemu-x86_64-static -cpu Nehalem /tmp/x.test   # forces SSE2 dispatch
go test -fuzz FuzzStreamingMatchesOneShot -fuzztime=60s .   # one target at a time
for a in 386 arm mips s390x ppc64 riscv64 arm64; do GOARCH=$a go test -c -o /dev/null .; done
```

That last loop matters more than it looks. `endian_slow.go` exists for the
big-endian and unaligned-access architectures, and none of them can be run
here — the least that has to hold is that the suite still *compiles* for them.
It did not, until recently: `internal/asmgen` is imported by `asmsim_test.go`,
so a constant in the generator that did not fit a 32-bit `int` took the whole
test binary down on 386, arm and mips.

qemu's TCG implements AVX2 but **not** AVX-512; an AVX-512 binary SIGILLs
there. That is why the simulator exists.

Four things check the kernels, and they do not overlap as much as they look:

| test | runs | oracle |
|---|---|---|
| `TestBackendsNative` | the linked `.s`, through the public API | C-derived vectors |
| `TestKernelsMatchPortable` | the linked `.s`, called directly | `generic.go` |
| `TestSimulatedBackends` | the generator's instruction stream | `generic.go` |
| `TestGeneratedFilesUpToDate` | the generator | the checked-in `.s` |

`TestKernelsMatchPortable` is the only one that reaches `accumBlocks` under a
custom secret. The reference vectors are one-shot, so a custom secret in them
enters through `hashLong` and the streaming walk is never keyed by it — which
is exactly where the `secretLimit` trap above would hide. The fuzz targets in
`fuzz_test.go` cover the same ground with the lengths and split points chosen
adversarially rather than by hand.

### The simulator

Each emitted instruction carries both its assembler text and a Go closure
implementing its semantics. `asmsim_test.go` executes the closures over a
modelled register file and address space, and compares the resulting
accumulators with the portable implementation and with the C-derived vectors.
Every backend goes through it, including AVX-512 and SVE2 at vector lengths
this machine does not have.

It has been mutation-tested: flipping a shuffle immediate or a shift amount in
the generator makes it fail. If you add an instruction emitter, its closure is
the only description of what that instruction means — get it right, and prefer
adding a case to an existing emitter over writing a new one.

The simulator proves the *instruction sequence* is correct. It says nothing
about encodings — those come from the system assembler, and the disassembly
comments in the `.s` files are the audit trail.

## Invariants

- **Wire format**: every constant in `generic.go` marked as such changes the
  hash if touched. `stripeLen`, `secretConsumeRate`, `midsizeStartOffset`,
  `secretLastAccStart`, `secretMergeAccsStart`, and the primes.
- **Go ABI, amd64**: R14 holds the goroutine pointer, R15 is reserved under
  dynamic linking, X15 is the zero register. None appear in `x86GPRNames` or
  the vector pools, and none should.
- **Go ABI, arm64**: R18 is platform-reserved, R27 is the assembler's
  temporary, R28 holds g, R29/R30 are frame and link. R12–R17 and R19–R25 are
  free. All V registers are scratch.
- Kernels are `NOSPLIT` with a zero frame and make no calls, so they need no
  stack maps. Keep it that way; adding a CALL inside one would corrupt the
  stack.
- `sum64` and `sum128` are `//go:nosplit`. The linker verifies the budget at
  build time, so a violation is a build failure, not a runtime bug.
- **Accumulator load width, amd64**: `LoadAcc` and `StoreAcc` read and write the
  eight accumulators in 128-bit pieces, never in one 256- or 512-bit access.
  The array they address comes from Go -- a copy of `initAcc`, or a `Digest`
  field -- and the compiler builds it out of 16-byte moves, because that is all
  the amd64 baseline has. A wider load spanning several of those stores cannot
  be satisfied from the store queue: it waits for them to reach the cache.
  Restoring the single wide access costs a quarter of a 256-byte hash. It is
  not free -- three extra instructions on AVX-512 -- but it is paid once per
  call and the stall it removes is not.
- **AVX-512 requires DQ, not just F**: the scramble multiplies whole 64-bit
  lanes with `VPMULLQ`, which is AVX512DQ. `pickBackend` checks for both. A
  machine with F but not DQ (Knights Landing) must land on AVX2.

## Performance notes

### arm64, measured on Neoverse N2 (2 vector pipes, 3.4 GHz)

The kernel obeys

	cycles/stripe = max(vector_ops / 1.5, instructions / 4)

which accounts for every variant measured on this core: the vector-only kernel
at 16 vector operations (10.7 predicted, 10.85 measured), a two-lane split at
13 (8.7 against 8.72), and the shipped four-lane split, which is instead
limited by instruction issue (30.5/4 = 7.6 against 7.73). Both constants were
measured, the second by padding the loop with `mov` instructions and watching
the slope: 0.246 cycles each, so 4.06 instructions per cycle.

Two consequences. Instruction count is the currency for the split kernel --
each one removed per stripe is worth 3.2% -- and moving lanes back to the
vector side loses even though it removes instructions, because 1.5 vector
operations per cycle is the harder limit. Four scalar lanes is the optimum of
the three splits that the register file and the pairing of `uzp` allow.

Hardware counters confirm it directly (perf works on this VM): the long path
retires 4.48 instructions per cycle with 0.25% frontend and 0.68% backend
stall cycles -- the 5-wide dispatch is saturated, nothing is waiting, and only
removing instructions can help. The mid-size ladder runs at 4.25 IPC, same
story, which is why the seed-free twins (two adds fewer per mix) were worth
9-14% where tree-summing its accumulator chain (same adds, regrouped) was
worth nothing.

- NEON runs at ~10.9 cycles per 64-byte stripe. Marginal cost is ~0.53 cycles
  per vector operation (≈1.9 ops/cycle) plus ~2.4 cycles fixed.
- The kernel is **not** load-bound (halving the loads made it slower, by
  creating a false dependency) and **not** loop-bound (unroll 2, 4 and 8 are
  within noise of each other). Reducing vector operation count is what pays.
- The deferred lane swap is worth ~17%: five vector ops per register per stripe
  instead of six, and two independent accumulator chains instead of one.
- The hybrid kernel (`neonhybrid`) moves half of each stripe onto the integer
  pipes. Measured here: +24% at 64 KiB. It is gated on MIDR because the trade
  is 8 vector operations for 20 scalar ones, which only pays where vector
  pipes are scarcer than integer pipes by more than 2.5x. See
  cpu_linux_arm64.go for the list and the evidence required to extend it.
- llvm-mca is how the untestable backends get analysed. It models an
  optimistic port bound -- it predicts 8.04 cycles/stripe for NEON on
  neoverse-n2 where the hardware gives 10.85 -- so use it for comparing two
  kernels on one core, not for absolute numbers. Extract a loop body with:

      go run ./internal/asmgen/gen -only neon -dump 0 \
        | awk '/^\.Lunroll/{inb=1;next} inb&&/b\.ge|jge/{exit} inb&&!/^\./{print}'
      llvm-mca -mtriple=aarch64 -mcpu=neoverse-n2 -mattr=+sve2 -iterations=100 ...

  Its per-core models are not equally trustworthy: the apple-m1 model charges
  a scalar `add` two units of a two-slot resource, i.e. one integer op per
  cycle on a core with six ALUs, so its hybrid numbers are meaningless. Check
  the resource-pressure table before believing a result.
- Streaming was profiled rather than guessed at, and the guess was wrong: for
  1 KiB writes, `runtime.memmove` was under 5% while the Go glue around the
  kernel (`consumeStripes`, the dispatch wrapper, the block-position modulo)
  was over 20%. Three changes took 1 KiB writes from 12.5 to 14.5 GB/s and
  256-byte writes from 7.7 to 9.5:
  1. The buffer became a 64-byte window followed by the staging area, so the
     last 64 bytes of the message are contiguous at `buf[bufUsed:]`. Write
     re-establishes both with one copy instead of three, and Sum64 never
     reassembles the final stripe.
  2. `absorb` lifts the secret pointer and block position out and calls the
     backend directly. The intermediate layer was ~9% on its own.
  3. The staging size went from the reference's 256 bytes to 512. It is a
     tuning parameter, not wire format.
- Costs of entering a kernel, measured with sum64's signature on this machine:
  an empty Go call is 1.77ns, `accumBlocks` with nbStripes=0 is 5.02ns, and
  with one stripe 7.70ns. So a call is 1.77ns of Go plus 3.25ns of kernel
  prologue and epilogue, and a stripe is ~2.5ns. Use those numbers before
  restructuring anything on the streaming path.
- Still on the table for streaming, both measured and rejected for now:
  0. The one-shot path for 1 KiB spends about 45ns: 38 in the kernel, 4 in
     mergeAccs, 2 copying initAcc. **Moving the merge into the kernel was
     implemented and measured: it is neutral to 0.8% worse, and was reverted.**
     The reason is visible in the generated code -- arm64 needs four
     movz/movk to materialize each 64-bit constant, where Go's compiler loads
     them from a read-only pool, and that cancels the call it saves. A kernel
     emitted as a pure code blob has no constant pool to reach for. Do not
     retry this without solving that; on x86, where movabsq is one
     instruction, it may still be worth measuring.
  1. `absorb` makes two kernel calls whenever anything is staged. Folding the
     second into the first through a seventh argument naming the straddling
     stripe **was implemented and measured: +6.7% at 1 KiB writes, -5% at
     64-byte ones, noise elsewhere, and it was reverted.** The regression is
     the extra argument, which every call pays whether it has a prefix or not;
     the gain only appears at write sizes large enough for one call to matter.
     It also consumes the last spare arm64 register (x26), leaving the ABI
     with no room. Skipping the call entirely -- wrong results, timing only --
     bounds the whole idea at +13% for 1 KiB writes.
  2. Absorbing that stripe in Go instead loses: `accumulate512Generic` is
     9.2ns against the 7.7ns call it would replace.
  3. Holding the accumulators in split form in the Digest, so no fold is
     needed per call, is worth about 1.2ns of the 3.25ns prologue. It costs a
     second Load/Store shape in the generator and a change to the marshalled
     state.
- `fixed.go` exists because half of a short hash is the call: an empty Go call
  with sum64's signature is 1.77ns against 3.0ns for the whole thing. Taking
  the input by value removes the length switch, and the default secret's
  bitflips are constants, which removes the secret pointer -- the argument that
  would otherwise push these over the budget. Cost 45-74 against 80, so keep
  an eye on `-gcflags=-m` when touching them; going one node over silently
  doubles their cost. TestBitflipConstants ties the constants back to kSecret.
- Two more streaming ideas measured and dropped, both in the staging path:
  replacing the short-write `copy` with inline word moves is worth nothing
  (memmove was not the cost), and Write's fast path cannot be inlined at all.
  A method call returning `(int, error)` costs 70 of the 80-node budget on its
  own, so no branch and no copy fits beside one. That is why Write is a thin
  wrapper around `write` rather than handling the common case itself.
- Go's inliner budget (80) drives several structural choices: the public
  entry points are thin so they inline; the 0..16-byte cases live inside
  `sum64`/`sum128` rather than in their own functions; `mixHalf` takes its
  crossover term as a parameter purely to stay under the budget. Check with
  `go build -gcflags='-m=2'` before restructuring these — an accidental
  non-inlined call in the short path costs 5-15%.

### amd64, measured on Zen 4 (Ryzen 7 8840HS)

The core retires **four 256-bit vector ALU operations per cycle**, and an
AVX-512 instruction on a 512-bit register counts as two of them. That single
number predicts the loop to within a few percent: count the arithmetic per
stripe, double for AVX-512, divide by four. AVX2 and AVX-512 were within 1% of
each other before any of this, for exactly that reason -- the same ten 256-bit
ALU operations per stripe either way, which is the algorithm's floor over 512
bits of data: xor, shift, multiply, and two accumulates.

**ALU operations, not instructions**, is the currency here, unlike on the
Neoverse split kernel above. A folded memory operand is cracked into a load and
an operation, so `op mem` and `load; op reg` do the same ALU work and differ
only in instruction count. Emitting half the stripes in the five-instruction
folded form and half in the six-instruction loaded form measured exactly
neutral. AVX2 is the exception: at 12 instructions per stripe it runs out of
floating-point dispatch slots before it runs out of ALU throughput -- which is
why it alone unrolls 8 rather than 4. At unroll 4 the loop's own four slots
per iteration are 8% of the stripe work, and halving them measured 4-5%
across every length; unroll 16 was neutral against 8, the loop-overhead
saving cancelled by code size. SSE2 sits just on the ALU side of the same
line (20 ALU ops against 28 instructions per stripe) and gains nothing from
unrolling.

- The AVX-512 kernel holds the **whole secret schedule in registers** for the
  default secret's sixteen-stripe block (`FastStripe`), gated on the input
  having `minFastBlocks` blocks -- see that constant for the measured
  crossover. The ALU work is identical either way; what it buys is one fewer
  load per stripe.
- The scramble is a bare dependency chain on one register with nothing to hide
  its latency behind, so its links cost what they cost. AVX-512 spends three:
  a shift, a `VPTERNLOGQ` folding the xorshift and the secret into one
  instruction, and `VPMULLQ`. Down from the seven the 32x32 assembly needs,
  which is about 8% of a 64 KiB hash.
- Folding the input into *both* operations, for a five-instruction stripe,
  measured 1.9% **slower**: nothing is saved in ALU terms, and the two
  dependent operations sit in the floating-point scheduler until their loads
  return -- the scheduler-full stall went from nil to 17% of cycles. Do not
  "simplify" `FastStripe` back into that shape.
- AVX2 gets no fast block and cannot: a block's secret is sixteen stripes at
  two registers each against fifteen usable ymm registers in total.
- Store-to-load forwarding on the accumulator array is worth more than any of
  the kernel work at short lengths; see the invariant above. It is why SSE2,
  whose kernel did not change, still moved.
- The portable accumulator loop walks the stripe one pair at a time rather
  than loading all eight words up front. The lane swap only couples d[i] with
  d[i^1], so a pair is done with its data as soon as it is absorbed; loading
  eight data words alongside eight accumulators spills the accumulators on
  amd64, which has ~14 registers to give. Pair-wise halves the spills and is
  worth 6% on the purego long path. It matters wherever the portable path is
  what runs: purego builds and every architecture without a kernel.
- The seed-free twins extend below 17 bytes: `sum64NS`/`sum128NS` are full
  transcriptions of the short cases with the seed terms deleted, and every
  unseeded entry point routes there without ever testing a seed. The 4..8
  case gains the most -- its seeded form spends a byte-reverse, a shift, a xor
  and a subtract deriving the mix from a seed that is zero -- and the twins
  are worth 8-10% on Sum64 at 4-16 bytes on a dispatch-saturated core, which
  closes the last of the gap to zeebo/xxh3 there.
- **Measured and rejected**: a `prefetcht0` a block ahead in the fast loop
  (neutral at 64 KiB, slightly negative at 1 MiB); a second pair of accumulator
  chains (the loop is throughput-bound, not latency-bound); mixing the five-
  and six-instruction stripe forms; an `and`-plus-`add` scramble on AVX2 that
  trades one operation per block for a mask register materialized on every
  call; a copy of both 64-bit ladders with the default secret compiled in as
  immediates, worth 1-3% -- a 64-bit immediate costs about what an L1 load
  costs, so this is far less than it looks.

The remaining gap to zeebo/xxh3 below 256 bytes is not what it appears to be.
At 128 bytes the two execute within 1% of the same number of instructions; the
difference is entirely IPC, 4.7 against 5.5. Two things are known to be in it,
one more call level and the two adds per chunk that a runtime seed costs.
Between them they do not account for all of it, and the rest has not been
found. Worth a fresh look, not another round of the hypotheses above.

## Reference vectors

`vectors_test.go` is generated by `ref/gen.c` against the xxHash v0.8.3 C
source; the header of that file has the two commands. Do not hand-edit the
vectors. If a change makes them fail, the change is wrong — they are the
definition.
