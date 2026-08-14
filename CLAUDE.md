# Working in this repository

xxhaste is an XXH3 implementation whose assembly kernels are generated. Read
this before changing anything under `internal/asmgen` or any `.s` file.

## Layout

| path | what it is |
|---|---|
| `xxhaste.go` | public API; `sum64`/`sum128` hold the 0..16-byte cases inline |
| `generic.go` | portable implementation: mid-size ladders, accumulator loop, convergence |
| `digest.go` | streaming `Digest`, a transcription of `XXH3_update`/`XXH3_digest` |
| `dispatch_{amd64,arm64}.go` | CPU detection and backend selection |
| `dispatch.go` | the same three entry points for purego / other architectures |
| `stub_{amd64,arm64}.go` | **generated** Go declarations for the kernels |
| `xxh_*_{amd64,arm64}.s` | **generated** kernels |
| `internal/asmgen` | the generator |
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
GOARCH=amd64 go test -c -o /tmp/x.test . && qemu-x86_64-static -cpu max /tmp/x.test
GOARCH=amd64 go test -c -o /tmp/x.test . && qemu-x86_64-static -cpu Nehalem /tmp/x.test   # forces SSE2 dispatch
```

qemu's TCG implements AVX2 but **not** AVX-512; an AVX-512 binary SIGILLs
there. That is why the simulator exists.

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

## Performance notes

Measured on Neoverse N2 (2 vector pipes, 3.4 GHz):

- NEON runs at ~10.9 cycles per 64-byte stripe. Marginal cost is ~0.53 cycles
  per vector operation (≈1.9 ops/cycle) plus ~2.4 cycles fixed.
- The kernel is **not** load-bound (halving the loads made it slower, by
  creating a false dependency) and **not** loop-bound (unroll 2, 4 and 8 are
  within noise of each other). Reducing vector operation count is what pays.
- The deferred lane swap is worth ~17%: five vector ops per register per stripe
  instead of six, and two independent accumulator chains instead of one.
- **Untaken optimization**: moving half a stripe onto the idle integer pipes
  measured out at ~7 cycles/stripe here (≈+45%), but it costs instruction
  bandwidth, and a 4-vector-pipe core (Neoverse V-series, Apple) would land at
  ~4.5 cycles/stripe against ~4 for the current kernel — a regression that
  cannot be measured on this hardware. Left out deliberately. Revisit with
  access to a wide arm64 core.
- Streaming with small writes is copy-bound, not kernel-bound: a 1 KiB Write
  copies ~320 bytes (buffer top-up, parked final stripe, remainder) against
  1024 bytes hashed. A design keeping a 64-byte rolling tail and a sub-stripe
  carry instead of the reference's 256-byte staging buffer would cut that to
  ~127, worth an estimated 5%. Not done; the buffer layout currently mirrors
  the reference, which makes it easy to check against.
- Go's inliner budget (80) drives several structural choices: the public
  entry points are thin so they inline; the 0..16-byte cases live inside
  `sum64`/`sum128` rather than in their own functions; `mixHalf` takes its
  crossover term as a parameter purely to stay under the budget. Check with
  `go build -gcflags='-m=2'` before restructuring these — an accidental
  non-inlined call in the short path costs 5-15%.

## Reference vectors

`vectors_test.go` is generated by `ref/gen.c` against the xxHash v0.8.3 C
source; the header of that file has the two commands. Do not hand-edit the
vectors. If a change makes them fail, the change is wrong — they are the
definition.
