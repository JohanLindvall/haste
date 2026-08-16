# Working in this repository

haste is a module of three hashes, each its own package, whose assembly
kernels are generated: `xxh3/` (XXH3, 64- and 128-bit), `xxh64/` (XXH64), and
`rapidhash/` (rapidhash). The module root holds no code. Read this before changing anything
under `internal/asmgen` or any `.s` file.

## Layout

| path | what it is |
|---|---|
| `xxh3/xxh3.go` | public API; `sum64`/`sum128` hold the 0..16-byte cases inline |
| `xxh3/fixed.go` | call-free entry points for compile-time-known sizes |
| `xxh3/generic.go` | portable implementation: mid-size ladders, accumulator loop, convergence |
| `xxh3/digest.go` | streaming `Digest`; same output as `XXH3_update`, different staging |
| `xxh3/dispatch_{amd64,arm64}.go` | CPU detection and backend selection |
| `xxh3/dispatch.go` | the same three entry points for purego / other architectures |
| `xxh3/stub_{amd64,arm64}.go` | **generated** Go declarations for the kernels |
| `xxh3/xxh_*_{amd64,arm64}.s` | **generated** kernels |
| `internal/asmgen` | the generator, for both hashes |
| `internal/cpu` | MIDR_EL1 on Linux/arm64, and "is this an Apple core"; used by both dispatchers |
| `xxh3/cpu_linux_arm64.go` | SVE2 detection, and the MIDR list gating the hybrid |
| `xxh64/` | XXH64: API and portable implementation (`xxh64.go`), `Digest`, per-arch dispatch, **generated** stubs and kernels, its own vectors and tests |
| `ref/gen.c` | emits xxh3's and xxh64's reference vectors from the C source |
| `ref/rapidgen.c` | the same for rapidhash |
| `rapidhash/` | rapidhash: API, portable implementation, **generated** kernels, vectors, tests |
| `bench/` | separate module; comparison against zeebo/xxh3, cespare/xxhash and the C reference |
| `bench/xxhash.h` | vendored reference C, v0.8.3, compiled in through cgo |
| `bench/sweep` | every length one at a time |
| `bench/mdtable` | `go test -bench` output to a markdown table |
| `bench/mkbenchmarks.sh` | a bench run's artifacts to `benchmarks.md` |
| `.github/workflows/` | `ci.yml` on every push, `bench.yml` by hand |
| `benchmarks.md` | **generated** by `bench.yml`; do not edit |

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

XXH64 has two of its own, in `xxh64/`:

```go
sum64(in, n, seed) uint64        // the whole hash, of any length, in one call
blocks(lanes, in, nbBlocks)      // the lane loop only, for Digest
```

`sum64` takes short inputs too, on purpose: a short XXH64 is nothing but the
tail the kernel has anyway, and the public entry points are inlinable wrappers
around one *direct* call to it, so a hash costs one call in total. Anything
between the caller and the kernel shows: an indirect call through a function
variable measured 2 cycles on an M2, a fifth of an 8-byte hash, and a Go
wrapper choosing between two kernels is past the inliner's budget. That is why
the arm64 kernel carries both forms of its lane round and takes the choice as
the sixth slot of the primes table the kernel already points at, rather than
being two kernels or taking an argument: an argument made every caller load a
global and every short hash carry a value only the lane loop reads. Moving it
into the table -- one load, only on the >=32-byte path -- took the short
lengths from 2-3% behind cespare/xxhash to 2-4% ahead on the N2.

## Regenerating assembly

```
go generate ./...                          # everything, both packages
go run ./internal/asmgen/gen -only avx512  # one backend
go run ./internal/asmgen/gen -only scalar  # the XXH64 kernels (both architectures)
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

`./...` covers `xxh64` too, which has the same shape of suite: vectors, kernels
against its portable code (both arm64 forms, forced through `setBackend`),
the simulator over both backends and both forms, streaming against one-shot,
fuzz targets. Cross-compile that package as well: it has its own
`endian_slow.go`.

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
| `TestKernelsMatchPortable` | the linked `.s`, called directly | `xxh3/generic.go` |
| `TestSimulatedBackends` | the generator's instruction stream | `xxh3/generic.go` |
| `TestGeneratedFilesUpToDate` | the generator, both packages' backends | the checked-in `.s` |

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

## CI and releases

`ci.yml` runs on every push and pull request: the suite in four build
configurations on amd64 and arm64 hardware, the SSE2 and AVX2 kernels forced
under qemu, a minute on each fuzz target, a test-binary build for every
supported architecture and OS, the generated-assembly check, and the
cross-implementation comparison.

Two jobs assert something about themselves rather than passing vacuously. The
cross job exists because `internal/asmgen` is imported by `asmsim_test.go`, so
a generator-only mistake takes the whole suite down on an architecture nobody
runs. The generate job fails if a backend *skipped* for want of a
cross-assembler, because a skip there is indistinguishable from a pass.

Nothing in the workflow names a package or a fuzz target. The fuzz job asks
`go test -list`, and the cross and qemu jobs ask
`go list -f '{{if .TestGoFiles}}...'`; all three fail if discovery returns
nothing. Every one of those was a hand-kept list first, and each stopped
covering a package the day one arrived -- `xxh64` for the fuzz job, `rapidhash`
for the other two.

### Repository metadata

The GitHub description, homepage and topics are part of how the module is
found and drift from it silently. As of the rename they are: the description
naming all three hashes and the generated-and-simulated kernels, the homepage
pointing at pkg.go.dev, and 19 of the 20 permitted topics -- the three hash
names, `wyhash` because rapidhash succeeds it, `code-generation` because that
is this repository's unusual property, the ISA and architecture names, and the
Go ones. `gh repo edit` is how they change.

### Tagging

A green run on `main`, pushed or dispatched, tags the next patch version.
Three things about it are worth knowing before changing it.

**It measures from the last tag, not from the push.** Rapid pushes supersede
each other's queued runs, and a superseded run never reaches the tag job.
Asking what *this push* changed then strands that commit behind the next
documentation-only push, which is what happened to b8003eb between v0.1.1 and
v0.1.2. Asking what has not shipped yet is self-healing.

**It creates the tag through the refs API, not `git push`.** A tag whose
commit's `.github/workflows` content differs from the branch tip's is refused
for `GITHUB_TOKEN`:

```
refusing to allow a GitHub App to create or update workflow
.github/workflows/ci.yml without `workflows` permission
```

There is no `workflows` key for the `permissions:` block to fix this with. It
is a platform rule, not a setting: an automatically issued token must not be
able to rewrite the automation that decides its own privileges. Every tag
through v0.1.16 predates the workflows changing, which is exactly why this
only started failing once they did — and why it presents as a bare exit 1 six
seconds in, with nothing in the annotation. If the API path is refused too,
the job prints the response and says what it would take: a PAT or App token
carrying the workflow scope, in a secret, in place of `GITHUB_TOKEN`.

**Only module files earn a version.** A README or workflow edit is not
something anyone can `go get`.

The line is `v0.x` deliberately. Bump the minor or major by hand
(`git tag v0.2.0 && git push origin v0.2.0`) and the automation continues from
wherever that lands.

### The bench workflow

`bench.yml` is dispatched by hand, sweeps every OS/architecture family the
standard runners offer, renders each log as markdown, and commits the
aggregate to `benchmarks.md` with `[skip ci]`. Those are shared VMs: good for
shape and for ratios within one column, not for absolute figures. Everything
in the performance notes below came from dedicated hardware instead.

It decides cgo by handing the compiler a five-byte program and seeing whether
a binary comes out. A runner with no C toolchain still has `CGO_ENABLED=1`,
and then `bench/` does not lose its C columns, it fails to build — which is
what `windows-11-arm` did. Asking whether `cc` is in `PATH` is a different
question and gets this wrong on an image that ships a stub.

## Benchmarking

Every number in the performance notes came out of this procedure; produce
comparable ones before believing a change, and expect a review to ask which
of these steps a surprising number skipped.

```
go test -c -o /tmp/x.test . && taskset -c 12 /tmp/x.test \
  -test.run='^$' -test.bench=<pattern> -test.benchtime=300ms -test.count=6
cd bench && go test -c -o /tmp/b.test .        # the comparison suite
go build -o /tmp/sweep ./sweep                 # every length, one at a time
go test -bench Compare -count 5 . | go run ./mdtable   # any log as a table
```

The comparison suite includes the reference C implementation itself:
`bench/xxhash.h` vendored at v0.8.3 — the revision the vectors came from —
compiled in through cgo with `XXH_INLINE_ALL`, and `-march=native` on amd64 so
it gets the same vector width the dispatched kernels pick. `TestSameAsC` holds
it to bit-identity at every length where any path changes, which closes the
loop the vectors open: not vectors taken from C once, but C in the same
process. It appears as the `c` and `c-xxh64` columns.

Read those two columns with one caveat: the one-shot rows cross the cgo
boundary once per hash, an overhead floor of tens of nanoseconds, so they mean
something from a few hundred bytes up and nothing below. The streaming rows
run their whole chunk loop on the C side and are comparable anywhere. Without
a C compiler the hooks are nil, every row guards on them, and those columns
simply disappear.

- **Pre-compile, pin, take medians.** Compilation is multi-core noise, so
  benchmark the test binary, not `go test -bench`. Pin with `taskset` to one
  core (its SMT sibling stays with the OS) so clock and thermals are one
  core's story; 300 ms × 6 counts and the median of the six is enough for
  1-2% resolution on this machine. Log the clock if the numbers will be kept:
  `scaling_cur_freq` sampled every few seconds during the run -- the Zen 4
  box holds 4.5-4.9 GHz pinned, and which end of that range decides a few
  percent. A laptop drifts; measure A against B back to back, never against
  last week.
- **A change is judged inside one binary where possible** -- its rows against
  the competitor's rows from the same run. Cross-binary deltas under ~8% at
  32..256 bytes are not results until they survive relinking; see the
  caller-alignment lottery below.
- **The caller-alignment lottery.** On the Zen 4, 32..256-byte results move
  ~0.65 ns with the *benchmark caller's* address (mod-64 phase 32 fast,
  phase 0 slow; both this library and cespare swing; no perf counter shows
  it). To sample layouts, add a `//go:noinline` dummy *function* of varying
  size to the bench package and relink -- padding data moves nothing, text
  placement is what matters -- and read the phase with `go tool nm` on the
  benchmark closure. Believe medians over 3+ relinked layouts, or a
  within-binary comparison, and nothing else at those sizes.
- **A single-length spike in the sweep is noise until it reproduces in
  isolation**: rerun `sweep -min N -max N` a few times before chasing it.
  Two 2-3x spikes have been chased to nothing this way (lengths 130 and 154
  in one run, 32 in another); they are the same lottery wearing a different
  hat. The sweep warms the core up first -- see the M2 note -- so early
  lengths are safe.

  **Each length gets its own allocation.** Slicing one buffer sized to the
  longest length in the run made a result depend on what else was being
  swept, and not equally: a 16 KiB prefix of a 128 KiB allocation measured
  cespare/xxhash at 21.5 GB/s where a 16 KiB allocation measured 26.6, while
  this library moved 2% between the same two. That inverted the standing at
  that size -- the shared-buffer sweep read xxh64 10% ahead of cespare at
  16 KiB where the compare suite read it 7% behind. The 0..256 range is
  unaffected either way, those buffers being small already.

  **The function-pointer harness that made the sweep's comparisons
  untrustworthy is gone**: each implementation now runs its own iteration
  loop, so the hash inside it is a direct call and the indirection is paid
  once per millisecond of work rather than once per hash. The two asymmetries
  below are what it used to cost, and are kept because they are the reason a
  sweep run from before that change cannot be compared with one after it --
  re-run it rather than trusting old output. On a Redwood Cove the harness
  cost haste 0.65 ns per hash against zeebo/xxh3's 0.15, enough to report
  haste 9% behind over 33..64 bytes where a direct call has it 6% ahead.
  Both measured asymmetries were in XXH64 at short lengths:
  - Our entry points are built to inline, so a direct call reaches the
    kernel in one call while a call through a function value takes two --
    the wrapper, then the kernel. cespare/xxhash's `Sum64` *is* the assembly
    symbol, so it pays one either way. Measured on an M2: our 12-byte hash
    2.15 ns direct, 2.71 through a function value; on Zen 3 and Zen 4 the
    sweep therefore reads 0.83-0.85x of cespare over 9..32 bytes where the
    compare suite reads 1.13x. The trade is deliberate -- the common case is
    the direct call -- but it makes the sweep's XXH64 column pessimistic on
    x86 by about one call.
  - On Apple silicon cespare's arm64 assembly costs 9-14 ns through a
    function value at any length needing two or more tail steps (12 bytes:
    10.42 ns against 2.46 direct), which makes the sweep's cespare column
    optimistic there by 4x. Not reproduced on Neoverse N2, Zen 3, Zen 4 or
    Ice Lake, and not diagnosed.
- **perf works here** (`perf_event_paranoid=2` allows user-space counting of
  own processes). Two traps: `perf stat -o FILE` truncates FILE when perf
  exits, so redirect the benchmark's stdout somewhere else or lose the
  iteration count; and raw counters divided by iterations overstate per-op
  cost by the calibration ramp's share (~25%). Take cycles/op as ns/op ×
  the measured clock (cycles ÷ task-clock), instructions/op as IPC ×
  cycles/op -- the ramp cancels -- and trust ratios between paired probes
  over absolutes. Branch-miss and frontend-stall counts are reliable nulls:
  when both are zero and cycles moved, suspect fetch geometry, not
  prediction.
- **Backends only diverge from 241 bytes** -- below that no kernel is
  entered, and the portable path measures identical to the assembly build.
  That makes the three backends a free noise gauge: forcing each in turn over
  the same lengths measures identical code three times, and on the Zen 4 the
  spread across them below 241 bytes is median 2.7%, p90 6.6% and 31% at
  worst. That is the error bar on any short-length number from a harness that
  measures one variant after another rather than interleaving them, and it is
  why bench/sweep walks the implementations inside each length.
  Force them with `setBackend` from a benchmark in the package
  (BenchmarkBackends is the pattern); purego needs its own binary
  (`-tags purego`). qemu is for correctness only: TCG timings mean nothing.

## Invariants

- **Wire format**: every constant in `generic.go` marked as such changes the
  hash if touched. `stripeLen`, `secretConsumeRate`, `midsizeStartOffset`,
  `secretLastAccStart`, `secretMergeAccsStart`, and the primes.
- **Go ABI, amd64**: R15 is reserved under dynamic linking; it appears in
  neither `x86GPRNames` nor the vector pools, and should not.
  R14 (the goroutine pointer) and X15 (the zero register) are different: they
  have those meanings in **ABIInternal only**, and every kernel here is ABI0.
  `cmd/compile/abi-internal.md` says so outright -- "In ABI0, these are
  undefined, so transitions from ABIInternal to ABI0 can ignore these
  registers" -- and the compiler backs it up, re-establishing both after every
  ABI0 call it emits (`XORPS X15, X15; MOVQ TLS, R14`, in
  `cmd/compile/internal/amd64/ssa.go`, `OpAMD64CALLstatic`). So an ABI0 leaf
  may clobber them, which is what cespare/xxhash has always done with R14.
  The XXH64 kernel takes R14 up on that and holds P4 there; nothing yet uses
  X15, which would give the vector pools a sixteenth register.
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
- **The XXH64 kernels are the ones that reference a Go symbol.** Generated
  bytes cannot carry a relocation, so anything naming `·primes` is in the
  prologue, which is Go assembly text. The two architectures reach it
  differently and must:
  - **arm64** loads `$·primes(SB)` into a register and reads five words off
    it, because five 64-bit immediates would be four instructions each in
    code with no constant pool.
  - **amd64 emits both forms and picks by CPUID vendor.** Intel takes the
    primes in registers, loaded RIP-relative, with P3 as a `movabs` at its
    two cold uses; everything else takes the pointer. Neither is better
    everywhere -- see the vendor-split bullet in the XXH64 notes. The
    kernel itself tests the form and jumps, so the entry point still
    reaches it in one direct call.

  `primes` in `xxh64.go` must stay `[7]uint64` in that order: the five
  primes, then the arm64 lane-round form, then the amd64 prime form. Only
  the last two are ever written, once at init and by setBackend in tests;
  the primes themselves are read-only. Both flags live here rather than in
  variables of their own so the kernel that reads one is reading a cache
  line it already wanted. A backend that wants constants in registers
  implements `TableLoader`; one that wants a pointer sets `TableGPR`. Both go
  through `FuncDef.Table`, and `asmgen.PrologueLoads` reports the register
  form so the simulator test can set up the same state -- the prologue is not
  part of the instruction stream, so without that the simulated kernel would
  run on zeroed primes.
- **XXH64's public wrappers must inline into their callers**, and reach the
  kernel in one direct call: `go build -gcflags=-m ./xxh64` must list
  `Sum64`, `Sum64String`, `Sum64Seed`, `Sum64SeedString`, `sum64` and
  `blocks` as inlinable. Adding a branch or a second callee to any of them
  costs the whole short-hash margin against cespare/xxhash (see below).
- **XXH64 kernels take any length**, zero included, and never read past the
  input: the tail is guarded bit by bit of n. The portable code walks by
  offset and never forms a pointer past the input either, because checkptr
  (on under `-race`) rejects one even if it is never dereferenced.

## Performance notes

### XXH64 (`xxh64/`)

XXH64 is a scalar hash and its lane loop has two bounds that no kernel can
move: each lane's chain per 8 bytes -- add, rotate, multiply, with the input
multiply off the chain -- and the integer multiplier's throughput at eight
multiplies per 32-byte block. Everything the kernels do is about not losing
anything on top of that.

- **The call is the short-hash budget.** An 8-byte XXH64 is ~2.3ns on an M2,
  and every layer between the caller and the kernel shows: an indirect call
  through a function variable measured 0.57ns (2 cycles) on top, a Go
  wrapper in between about the same. So the kernel takes every length, the
  entry points are inlinable one-call wrappers, and the arm64 form choice is
  an argument, not a second kernel. With that, 4..16 bytes are level with
  cespare/xxhash on the M2 (2.29 against 2.30ns), which is a direct call to
  hand-written assembly.
- **arm64 lane round, two forms, chosen by core.** The fused spelling `madd v,
  x, P2, v; ror; mul` (cespare's) chains at 7 cycles per block on an Apple M2
  because the madd's addend comes from a plain `mul` and Apple's cores forward
  a multiply-accumulate's addend in one cycle only from another
  multiply-accumulate (probed: `mul` then `madd` on one register is 6 cycles
  per pair, `madd` after `madd` 1). The split `mul x, x, P2; add; ror; mul`
  chains at 5: 20.9 GB/s against 15.9, +31% from a kibibyte up. It costs one
  instruction more per lane, which is why it is not the default: on an
  issue-bound N2 the fused form already runs at its 4-cycle chain bound
  (cespare measures 26.5 GB/s there, 4.1 cycles per block at 16 instructions,
  i.e. at issue), and four more instructions per block would be about −11%.
  Not measured on an N2; the fused form is exactly cespare's proven shape, so
  the non-Apple default is the safe side. Apple is identified through
  `internal/cpu` (always, on macOS; MIDR implementer 0x61 on Linux).
- **On the M2 the forms cross at about 128 bytes**: fused is 3-4% faster at
  32-64 (fewer instructions on a throughput-bound call), split 9% ahead at 256
  and 30% beyond. Split is the Apple choice for that reason. Against cespare
  on the M2: level to 16 bytes, −4% at 32, −2% at 64, +4% at 128, +8-10% at
  240-256, +30% from 1 KiB, streaming +6% at 64-byte writes and +30% from
  1 KiB. The residual −4% at 32 bytes is the split form's four extra
  instructions plus the two-block unrolling's odd-block prologue on a
  one-block input; measured, accepted.
- **amd64 is one form** and imul-bound: eight `imul` per block at one per
  cycle on every current core, so ~8 cycles per 32 bytes against a 5-cycle
  chain, and cespare's kernel is already there. Measured on a Zen 4
  (2026-08): the long loop runs at 1.01 cycles per imul -- the wall, exactly
  as modelled -- and every length from 33 bytes up is within +/-2% of
  cespare.
- **The prime form is a vendor split, and the kernel carries both.** Holding
  the primes in registers is worth 12-16% over 32..256 bytes on an Intel
  Redwood Cove and costs a Zen 3 5-17% over 8..256; reaching them through a
  pointer is the reverse. Neither is understood. The generator emits
  `sum64<B>Ptr` and `sum64<B>NSPtr` beside the register-form kernels, and
  `dispatch_amd64.go` writes `primes[6]` from the CPUID vendor string at
  init: Intel gets registers, everything else gets the pointer, which is the
  older and more widely measured of the two.

  The vendor is coarser than the evidence -- the loss is Zen 3's alone, and
  Zen 4-class parts read level either way -- and it is free to be coarse for
  that reason: the pointer form costs a Zen 4 nothing. Narrowing it further
  would not be a family check either, since Zen 3 and Zen 4 share family
  0x19 and part below it, so it would be a model list on the terms
  `cpu_linux_arm64.go` sets for the MIDR one.

  The choice is made *inside* the kernel -- `CMPB ·primes+48(SB), $0` and a
  jump to the other symbol -- for the reason the arm64 lane round is:
  selecting in Go costs either an indirect call or a second callee, and a
  second callee pushes the entry points past the inliner's budget, which is
  more than either form is worth.

  **It is not free.** On the fall-through path it costs, measured against a
  build with the split compiled out, median of nine, both alignment phases:
  +3 to +7% at 4..16 bytes, ~1% at 32, nothing from 64 up. Three placements
  were tried -- the test ahead of the prologue, after it, and with the flag
  moved into the primes' own cache line -- and all three measure the same,
  so it is the load and the branch, not where they sit. The trade is a
  short-hash cost on one vendor against a 5-17% regression across 8..256
  bytes on the other; if someone with AMD hardware can find which half of
  the form Zen actually dislikes, a form that suits both would beat this.

  Within one binary, which is the comparison that has no layout in it,
  `BenchmarkBackends` puts the register form ahead of the pointer form on
  this Intel core by 13.3% at 32 bytes, 15.3% at 64, 9.3% at 256, 2.9% at a
  kibibyte and nothing at 64 KiB.
- **Offloading the four off-chain `in*P2` products to the vector unit is
  worth +20%, and the transfers decide it.** This was the standing "needs an
  Intel core to measure" item; a Redwood Cove has now measured it. The lane
  loop alone, cycles per 32-byte block, median of five, pinned, over 2048
  blocks (32 blocks in brackets):

  | lane loop | cyc/block |
  |---|---|
  | shipped, eight `imul` | 8.35 [8.46] |
  | four `imul`, products from AVX2, back via `vmovq`/`vpextrq` | 8.16 [8.23] |
  | four `imul`, products from AVX2, back through memory | **6.98** [7.56] |
  | eight `imul`, AVX2 products computed and thrown away | 8.26 [8.36] |

  Three things fall out. The **emulation is free**: seven vector operations
  per block on top of the full scalar loop cost nothing at all, because they
  hide behind the eight-`imul` bound. The **register extracts are not**:
  `vmovq` + `vpextrq` + `vextracti128` give back almost the whole gain, which
  is the unknown this note flagged, and the answer is that they are the wrong
  transfer. **Through memory it lands**, 8.35 to 6.98, +20%.

  Not +60%: 6.98 is still two cycles above the 5-cycle chain the model says
  should then bind, and where those go has not been chased. An all-vector
  loop remains out (Intel's `vpmullq` is 15-cycle latency and the chain would
  eat it), and AVX-512's single `vpmullq` for the products is untested --
  this box has none.

  What it would cost to ship, none of it measured: a 32-byte scratch slot,
  so the kernel stops being `NOSPLIT` with a zero frame; runtime AVX2
  detection, so `sum64` stops being one direct call to one kernel, which is
  the property the whole XXH64 design is built around and which the arm64
  form choice was pushed into the primes table to preserve; and a short-hash
  path that must not pay for any of it, since the offload loses below a
  handful of blocks. Worth it only if someone wants XXH64 long-input
  throughput badly enough to spend that, and it still wants a Zen 4 reading.
- **The dual kernel costs 1-2% where a single block cannot amortize it**, and
  that is the whole of it: measured with direct calls on the N2, the form load
  and its branch are 2.1% at 33 bytes and 1.3% at 37 -- one 32-byte block plus
  a short tail -- and nothing at 31, where the block loop is not entered. They
  are what buys +31% on Apple cores.

  An earlier version of this bullet had xxh64 3% *behind* cespare/xxhash at
  those two lengths, chased through relinked layouts and a `Dual()` false
  build. That number was the sweep harness, not the kernel: it calls through a
  function value, which our inlinable entry point pays twice and cespare's
  assembly symbol once. Direct calls put us level or ahead at every length
  from 31 to 88 -- +0.7% at 33, -0.9% at 37, -1.6% at 88, -1.8% at 64. When a
  sweep number disagrees with the compare suite about *this* library against
  cespare, the compare suite is right; see the harness note under
  Benchmarking.

  Three things were tried against the 1-2% and are not worth keeping: turning
  `TailMaskSkips` on for arm64 (33 unchanged, 88 worse -- branch cost on this
  core is not what it is on x86), hoisting the form load above `InitLanes` to
  cover its latency (neutral; the out-of-order window was already covering
  it), and inverting the branch so the fused form falls through and the split
  form is the taken side (neutral -- it is one taken branch either way).
- **Do not move the form choice out of the kernel.** It looks like the obvious
  way to spare the cores that do not need it, and every mechanism costs more
  than the 0.18ns it saves, because all of them move the choice into the
  caller's path where every length pays it:
  - Two kernels with a branch in Go: `sum64` then needs two call nodes, about
    114 against the inliner's 80, so the wrapper stops inlining and every hash
    pays a whole extra call level -- ~1.5ns, eight times the saving.
  - A function variable: an indirect call, measured at 2 cycles (0.57ns) on
    the M2, three times the saving and again on every call. It also gives up
    "one direct call in total", which is why short XXH64 is competitive here
    at all.
  - Build tags (`darwin/arm64` split, `linux/arm64` fused): does not help the
    Neoverse case at all, because Linux runs on Apple cores too -- Asahi is
    exactly why the MIDR check exists -- and forcing it there would trade
    0.18ns for the 31% the split form is worth on those cores.
  - Emitting the cold form out of line is the one clean option, and it **was
    implemented and measured: neutral, on the branch `xxh64-out-of-line-form`.**
    `emitBlockLoops` returns a function that emits the other form where the
    caller has cold code, and `emitSum64` places it just past the jump the
    long path already makes over the short one -- so the default path falls
    straight through and pays no taken branch at all, which the naive inline
    arrangement cannot do. Within one binary Sum64 is identical to three
    decimals at 31, 32 and 64 bytes, and the sweep leaves 33 and 37 where
    they were. That is the ceiling this note predicted, half of 0.18ns, under
    the noise floor. The branch is kept as the worked answer, not merged.
- **amd64 emits an unseeded twin of the one-shot kernel.** `sum64ScalarNS`
  is `sum64Scalar` with the seed folded to zero: no third argument to load,
  `h` starts from the fifth prime instead of `seed + P5`, and the lanes start
  from the primes alone (six instructions against eight). `Sum64` and
  `Sum64String` reach it in one direct call and `Sum64Seed` still reaches the
  seeded kernel, so which one a caller gets is settled at compile time and
  costs no branch -- the same trade the parent package makes with `sum64NS`.
  The "+1.0 to +1.9% at every length" once recorded here was measured on a
  build where `Sum64` still called the seeded kernel, so the twin was
  unreachable and that number was rebuild drift -- its flatness across
  lengths was the tell, since folding a seed can only pay where the hash is
  short. See fc5d086 for the wiring that was missing. Re-measure before
  quoting anything here. Gated on `UnseededTwin`, off for arm64
  until an arm64 is measured: three-operand adds make its lane setup cheaper
  already, so the twin has less to win there.
- **The amd64 tail's combined-mask skips are generated but off.**
  `TailMaskSkips` emits tests of n against 31, 24, 7 and 3 ahead of the
  per-bit guards, so a trivial tail pays one or two taken branches instead of
  up to five. They were on while the prologue built the five primes with
  `movabs` -- fifty bytes of ten-byte instructions -- where they were worth
  12-17% of a 4- or 8-byte hash. Holding the primes in registers removed that
  prologue and with it what they were paying for; what is left is their cost,
  two not-taken tests on every length whose tail runs more than one step.
  Re-measured after that change, four relinked layouts each, every length
  1..40 with direct calls: off beats on by 4.8 points over 9..16 bytes and
  3.2 over 17..32, for +1.2% against cespare across 1..40 where on reads
  -0.8%, and the per-layout aggregates do not overlap. Only 4 bytes prefers
  them, by 8.5 points; two narrower variants (the two entry skips alone, and
  everything but the n&7 skip) were generated and measured and neither
  recovers that, because the win needs the whole chain. arm64 has always had
  them off.
- **What is left against cespare on amd64 is the tail's shape, and it is one
  open item.** Per length, four layouts, current build: +1.3% over 1..40,
  +3.4% over 9..16 and +3.6% over 17..31 -- and two windows behind.
  - **1, 2, 4, 5, 8 bytes: -5 to -7%.** These are the lengths whose tail runs
    one or two steps, and where our unrolled chain walks all five bit guards
    while cespare's loops walk three pointer compares. The mask skips above
    fix exactly these and cost more than they return elsewhere, so the
    unexplored idea is a two-level tail: dispatch once on `(n>>3)&3` for the
    eight-byte steps and once on `n&7` for the rest, which would execute two
    or three tests at every length instead of five. It is more code and has
    not been written.
  - **32, 36, 40 bytes: -3 to -6%.** One block plus a nil, four- or
    eight-byte tail. 33, 34, 35, 37 are level or ahead, so this is not the
    block loop; it is the same guard-count story with the block's cost
    hiding it less than a longer tail would.
  Both reproduce on every layout, so neither is the caller-alignment
  lottery. Everything from 9 to 31 bytes and from 41 up is level or ahead.

- **Benchmarking 32..256-byte XXH64 on Zen 4 is a caller-alignment lottery.**
  Both this kernel and cespare's swing ~0.65 ns (6 cycles) at those lengths
  with the *calling function's* address: mod-64 phase 32 is the fast mode,
  phase 0 the slow one, verified by correlating `go tool nm` addresses of the
  benchmark closure across relinked binaries. No counter shows it -- zero
  mispredicts, zero frontend-stall cycles, icache quiet -- and PCALIGN on the
  kernel itself does nothing, because the phase that matters is the
  caller's. It is bimodal, not a spread: six layouts produced exactly two
  outcomes, one per phase, and several sizes change sign between them (the
  table above). So a median over a handful of relinked layouts is only as
  good as its phase balance -- sample both phases and mean them, or quote a
  band. Single-binary comparisons at these lengths carry +/-5-8% and settle
  nothing.
- **Unroll two, odd block first.** The loop is chain-bound, not
  overhead-bound, but the N2 model prices the two loop instructions per block
  at ~11% of the fused form's 16-instruction block, and pairing halves them.
  `ldp x` reaches ±512 bytes, which allows the pair; unroll four would need a
  second base register.
- **Streaming**: `Digest` stages up to a block and hands whole blocks from the
  caller's slice straight to the `blocks` kernel; the tail and merge run in
  Go once per Sum64.

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
- **Every kernel this machine can run is at a bound, and the counters say
  which.** A sweep of all reachable profiles at 64 KiB, `perf stat` on each:

  | profile | GB/s | IPC | be-stall | cyc/stripe | instr/stripe | bound |
  |---|---|---|---|---|---|---|
  | neon | 20.76 | 3.51 | 12.5% | 10.48 | 36.8 | vector pipes: 16 ops / 1.5 = 10.7 |
  | neon-hybrid | 28.53 | 4.07 | 6.4% | 7.63 | 31.0 | dispatch: 31 / 4 = 7.75 |
  | neon-hybrid2 | 25.90 | 3.70 | 12.0% | 8.40 | 31.1 | vector pipes: 13 ops = 8.7 |
  | sve2-vl128 | 20.88 | 2.88 | 24.6% | 10.42 | 30.0 | vector pipes, NEON's 16-op floor |
  | purego | 15.11 | 5.12 | 3.2% | 14.40 | 73.7 | dispatch, 5-wide saturated |
  | xxh64 madd | 26.88 | 3.84 | 12.4% | 4.05/block | -- | lane chain, 4.0 modelled |

  Each row lands within 3% of the model in the same line, so there is no
  headroom to find on this core: a backend stall here is a pipe at its
  throughput, not a latency to hide. SVE2's 24.6% is the clearest case -- it
  issues the *fewest* instructions of any vector kernel and is still the same
  speed as NEON, because at VL=128 both sit on the algorithm's 16-vector-op
  floor. purego is the mirror image: 3.2% stalls at 5.12 IPC is a path that
  never waits and simply issues 2.4x the instructions.
- **purego cannot shed its truncation from Go source.** Its lane is `eor`,
  `lsr`, `movwu`, `madd` where the kernel's is `eor`, `lsr`, `umaddl` -- the
  `movwu` is 8 instructions a stripe, 15% of the loop, and it is there because
  Go will not match `uint64(uint32(k)) * uint64(uint32(k>>32))` to UMULL,
  whose operands are already the W views. It does emit UMULL when both
  operands *arrive* as uint32 (verified on go1.26), so the fix is a compiler
  rule, not a spelling: every source form tried here -- explicit uint32
  locals, a mask instead of a conversion, a uint32-argument helper -- still
  goes through MOVWU once the value comes from a uint64.
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
- Two streaming changes landed 2026-08, measured on the Zen 4 and applied to
  all architectures; the small-write numbers below and in the M2 section
  predate them and need re-measuring on those machines:
  1. **absorb drains small writes with one kernel call**: when p fits the
     staging area (len(p) < internalBufferSize-63), the staged whole stripes
     are absorbed and p is re-staged, instead of the two-call
     staged-then-direct split. The call's fixed cost (accumulators loaded
     and stored, prologue) was a third of a 256-byte Write on the Zen 4.
     16-byte writes went +29% ahead of zeebo, 64-byte from -5% to +10%;
     256-byte writes trade the saved call for ~190 more copied bytes and
     measure a wash inside the caller-alignment noise; >=449-byte writes
     keep the old path untouched. This supersedes half of the rejected
     seventh-argument idea below: the small-write case now gets its one-call
     drain without the argument every call would pay for.
  2. **absorb dispatches through accumBlocksStream**, a function variable on
     amd64 (the dispatch switch was 18% of a 256-byte Write there; an
     indirect call is two cycles) and an inlined wrapper everywhere else, so
     arm64 and purego compile to exactly the code they had. consumeStripes
     must keep calling the switch: a call through a function variable makes
     its arguments escape, and digestLong's accumulator copy lives on the
     stack (TestNoAlloc is the tripwire).
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
     tuning parameter, not wire format -- though `marshaledSize` counts it, so
     changing it does invalidate marshalled state.

     **It is 1024 now**, raised on a Redwood Cove where 1024 was better or
     level at every write size: −17.7% at 64-byte writes, −11.6% at 256,
     −3.5% at 4 KiB, +0.3% at a kibibyte. That is a block, and the same
     amount zeebo/xxh3 stages. A further doubling to 2048 was better again
     below 64 bytes and worse at 256 and above, which is the trade this note
     originally recorded against going past 512.

     **Measured on arm64 since**: on an M2 P-core, 1024 against 512 with
     everything else current is +7.6% at 16-byte writes, +9.5% at 256, and
     -4.0% at 64, with a kibibyte and above unchanged -- back to back, six
     samples each. So the trade holds there too except at exactly 64-byte
     writes, which is the one write size where staging a block rather than
     half of one costs anything on that core. The N2 has still not been
     re-measured. The interaction with the small-write drain -- whose
     `len(p) < internalBufferSize-63` gate now admits writes up to 961 bytes
     rather than 449 -- was checked on the Redwood Cove and costs nothing
     (−14.1% at 64-byte writes, −13.5% at 256, −0.5% at a kibibyte with the
     drain in place), but the Zen 4 numbers in that bullet were taken at 512
     and have not been repeated at 1024.
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
- An exhaustive per-length sweep (bench/sweep, every length 0..255, both
  widths) found no anomalies: within every size class up to 128 bytes the
  spread across lengths is 1-2% -- no alignment or odd-length pathologies --
  and 129..240 ramps smoothly with the tail. After the seed-free twins were
  extended below 17 bytes, the only lengths more than 3% behind zeebo/xxh3
  are 0..3, in both the 64- and 128-bit hash -- about 0.3ns of the signature
  cost the custom-secret support carries. Every other length, 4..255, is a
  tie or ahead: +10% at 17..32 rising to +39% through 129..240, +38% median
  over 17..255.

  Those figures predate the sweep harness being made direct-call; see the
  benchmarking section. Re-run before quoting them.
- **Writing the convergence out cost the custom-secret short paths about
  5%.** Measured on a Zen 4 after that change, as a within-binary ratio so
  layout and drift cancel: `Sum64Secret` against `Sum64` at the same lengths
  was 0.96x at 0..16 bytes and 0.99x at 17..32 before, and is 1.06x and 1.05x
  after -- a custom secret used to be free and now is not. Three independent
  A/B pairs against the previous build reproduce it at +4.2%, +7.1% and +5.0%
  while `Sum64` on the same cells stays flat. The mechanism is that both
  short rungs live in `sum64NS`'s own body, so they pay for the register
  pressure the written-out convergence adds; `&kSecret` is rematerializable
  where a caller's secret pointer and length are not. It is kept: the same
  change is worth 5% on this core's own long path (241..512 bytes) and 9% at
  256 bytes in `sum128NS` upstream, and a custom secret is the rarer call.
  Anything that revisits it should measure `Sum64Secret` at 17..32 alongside.

- Specializing the 0..16 paths on the default secret (compile-time bitflips,
  guarded by seed==0 && sec==&kSecret) was measured and rejected: only empty
  input won (+13%), 4..16 regressed 3-6%. The guarding compares sit on the
  critical path, while the loads they remove are L1-hot kSecret words that
  ran in parallel all along. The same constants pay in fixed.go because the
  call disappears with them, not because the loads do.
- Regrouping an accumulator chain has now lost three times on this core, and
  the reason is the table above: at 4 IPC with a full out-of-order window
  those adds were never the critical path. Tree-summing the 17..128 ladder was
  2% slower, a two-accumulator tail in the 129..240 ladder 1.4% slower
  (16.00 -> 16.22ns at 240 bytes), and only mergeAccs -- which runs once, with
  nothing after it to overlap -- gained from it.
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

### arm64, measured on Apple M2 (Avalanche P-core, 3.49 GHz, macOS)

macOS has no MIDR and no SVE, so this core runs the plain NEON kernel, and
the numbers below are that kernel's unless said otherwise. The core was
characterized with the instructions the kernels actually use, in a scratch
harness of GNU-syntax loops assembled with clang; the model that came out
predicts every kernel here to within a few percent, and it is a different
model from the N2's.

- **Vector: four pipes, uniform.** Every NEON operation the kernels issue
  -- `eor`, `add`, `ushr`, `shl`, `uzp1/2`, `ext`, `xtn`, `shrn`, `umlal`,
  `umull`, `mul.4s` -- runs at exactly 4 per cycle; latency 2 for the logic,
  shifts, permutes and adds, 3 for the multiplies. A stripe's 16 operations
  take 4.00 cycles with or without their loads.
- **Front end: 8-wide, sustaining ~7.0-7.3 in the stripe loop.** Padding
  the NEON loop with `nop`s is free until ~26 instructions per stripe, then
  costs one cycle per ~7.2 instructions. `nop` and `movi #0` are otherwise
  eliminated; `mov` between registers too.
- **Loads: ~2.8 per cycle.** `ldp q` is two load micro-ops, `ldp x` one;
  a 16-byte load at 8 mod 16 costs nothing extra. `ld2` (deinterleaving
  load) takes two vector-pipe slots on top of its loads, so it cannot
  replace the `uzp` pair. Measured and dropped.
- **Integer multiply-accumulate is the trap.** `umaddl`/`madd` issue once
  per cycle (`mul`/`umull` twice), and a multiply-accumulate whose addend
  comes from anything but another multiply waits the full 3-cycle
  multiplier latency: `add` then `umaddl` on one accumulator is 4 cycles per
  pair, `umaddl` after `umaddl` is 1. Simple ALU ops run on 6 pipes;
  shifted-register forms (`eor x, x, x, lsr #n`) on about 2.
- **Calls are cheap here:** an empty Go call with sum64's signature is
  0.86ns (3 cycles) against 1.77 on the N2; `accumNEON` with no stripes is
  3.44ns, one stripe 4.36; `mergeAccs` 2.6ns; the `acc := initAcc` copy
  1.0ns.

What that makes of the kernels:

- The NEON long path is **exactly vector-bound**: 4.0 cycles per stripe in
  the loop, plus the per-block scramble and materialize. With the reference's
  three-operation multiply in the scramble (`xtn`, `mul.4s` by `{0,P}`,
  `umlal`) that is (16 stripes x 16 + 4 x 8) / 4 / 16 = 4.5 cycles per stripe
  all-in against 4.72 before it: 1385ns to 1330ns at 64 KiB, +4%. Nothing
  else on the vector side is left to remove -- the 16 operations per stripe
  are the algorithm's floor over 128-bit registers -- and the only further
  block-level saving would be `eor3` (FEAT_SHA3, present here) folding the
  xorshift and secret xor, 4 operations per block, ~1.4%, not worth a
  feature gate.
- The **four-lane split kernel loses by 4-10%**, as CLAUDE.md predicted, but
  for a reason the vector-pipe count does not capture: its four `umaddl` per
  stripe alone are 4 cycles at one per cycle, and each lane's
  add-then-multiply-accumulate chain is another 4. It is also 9-17% behind
  on the E-cores.
- A **two-lane split** (`neonhybrid2`, generated and measured, not
  selected) removes both integer bottlenecks and cuts the vector work to 13
  operations per stripe, and then hits the front end instead: 27.5
  instructions per stripe at ~7.1 per cycle is 3.9 cycles, which is where it
  measures. Getting there needed the lanes' product added to the data word
  before the accumulator (`laneMixReassoc`) and unroll 8, the most `ldp x`
  reaches. Against the NEON kernel: +5.6% at >=64 KiB, +5.0% at 16 KiB,
  +3.3% at 4 KiB, -2.4% at 1 KiB, -5.4% at 256 bytes; level all-in on the
  E-cores. Not enough to dispatch on this core. The same arithmetic says it
  wins on a wider front end -- 27.5 instructions at 9 per cycle would be
  3.1 cycles against NEON's 4.0 -- so an M4-class core is where to measure
  it next, before enabling it anywhere. On the N2 the vector-operation model
  puts it behind the four-lane kernel (8.7 against 7.7 cycles per stripe).
- Both kernels bracket the M2 optimum from opposite sides -- one on the
  vector pipes, one on the front end -- and no split of the eight lanes
  lands between them, because lanes move in pairs.
- **240 to 256 bytes is a 30% cliff here** (8.7ns to 11.2ns) where the N2
  and Zen 4 show a few percent. The ladder is fast on this core, and the
  accumulator path's fixed cost is not: at 256 bytes the kernel is 26.7
  cycles, mergeAccs 9.1, the initAcc copy 3.6 -- 39.4 of the 39.2 measured
  -- and most of the kernel's share is one latency chain from the last
  stripe's `umlal` through `ext`, `add`, the store, the forwarded load and
  the merge's multiplies, which five stripes of throughput work cannot hide.
  The in-kernel merge rejected on the N2 for its constant materialization is
  a weaker objection here (a call is 3 cycles, four `movk`s half a cycle),
  but it would trade the store-and-forward for eight vector-to-GPR moves;
  unmeasured.
- Against zeebo/xxh3 on this core: +4-5% at 4-16 bytes, +18% at 32, +23%
  at 64, +37% at 128, +51% at 240, +17% at 256, +47% at 1 KiB, 1.6x from
  4 KiB up (47 GB/s against 30 at 64 KiB); the 128-bit hash the same shape.
  Ahead of cespare's XXH64 everywhere, 1.2x at 4 bytes to 3x at 64 KiB.
  The per-length sweep leaves 1..3 bytes 3-8% behind, as on Zen 4, and
  everything else level or ahead. Streaming: 11.1 GB/s at 64-byte writes,
  29.6 at 1 KiB, 40.6 at 4 KiB, 45.7 at 64 KiB.
- `bench/sweep` charged its first few lengths for the core's clock ramp
  (length 0 read 12ns cold against 2.4 warm); it now warms up first. Check
  that before trusting any single early length from an older run.
- The E-cores, measured under `taskpolicy -b` (which also clocks them down
  to ~0.95 GHz, so only the ratios mean anything): ~2 vector operations per
  cycle, ~6.5-wide, `umaddl` at 1.2 per cycle. NEON and the two-lane split
  tie all-in; the four-lane split loses.

### amd64 at scale, measured on GitHub runners (shared VMs)

Eight cores have been sampled across three instruction sets, the x86 ones on
four-vCPU GitHub runner VMs. The VMs turn out to be steady for this workload -- across four
independent Zen 3 allocations both our numbers and zeebo's repeat to +-0.3%
up to 64 KiB -- so the ratios below are real, and only the absolute
nanoseconds carry the VM's clock.

- **Dispatch picks AVX-512 on Intel and it is the wrong kernel on two of
  the three.** At 64 KiB: Emerald Rapids (Xeon 8573C) 1,222 ns AVX-512
  against 1,159 AVX2; Granite Rapids (Xeon 6973P-C) 1,168 against 1,061;
  Ice Lake-SP (Xeon 8370C) the other way round, 1,152 against 1,207. The
  inversion follows the Golden-Cove-derived server line and not the older
  Ice Lake. A vendor-only rule in `pickBackend` (Intel implies AVX2) buys
  5-9% on two cores and costs 4% on the third; getting all three needs a
  CPUID model check, which this repository does not have today. **On AMD the
  current choice is right**: an EPYC 9V74 runner that exposed AVX-512
  (another had it masked off by the host) runs our AVX-512 kernel 11% faster
  than our AVX2, 825 ns against 929.
- **XXH3 is behind zeebo/xxh3 from 16 KiB up on four of the eight cores
  sampled, and it is not an Intel property.** Ratios at 16 KiB / 64 KiB /
  1 MiB: Granite Rapids 0.85x / 0.76x / 0.72x, Emerald Rapids 0.85x / 0.83x
  / 0.77x, Ice Lake-SP 0.92x / 0.88x / 0.86x -- **and EPYC 9V45 1.06x /
  0.87x / 0.81x**, an AMD part. Ahead everywhere at those sizes: M2 1.66x,
  N2 2.06-2.28x, Zen 3 1.05-1.12x, EPYC 9V74 1.14-1.15x. Below 4 KiB every
  one of the eight has us ahead, by up to 2.47x.

  What the four have in common is not a vendor but a fast kernel: the 9V45
  runs our AVX-512 at 640 ns per 64 KiB, the quickest of any sample, and
  loses anyway because zeebo's AVX2 does it in 552. Where our kernel is
  slower in absolute terms -- Zen 3 at 1,097, the 9V74 at 825 -- we are
  ahead. The crossover sits between 4 and 16 KiB, which is also where the
  input stops fitting L1. Choosing AVX2 on the two Intel parts that want it
  closes part of the gap and not all of it (Emerald Rapids 0.83x to 0.88x at
  64 KiB), and on the 9V45 AVX-512 is already our better kernel. This is the
  open performance item, and "Intel" was the wrong frame for it.
- Candidates for the remainder, none tested: 512-bit licence behaviour on
  the fast-block path, our per-stripe secret load against whatever zeebo
  keeps in registers, and Intel's three-port 256-bit issue against Zen's
  four. It wants an Intel machine with counters -- and note that the Redwood
  Cove used elsewhere in these notes is a Meteor Lake client part with **no
  AVX-512 at all**, so it can say nothing about any of this. Every Intel
  core that runs the AVX-512 kernel has so far been a runner.
- The comparison that matters here is within one binary: `BenchmarkBackends`
  forces each kernel through `setBackend` in the same process on the same
  core, which is what the AVX-512-against-AVX2 numbers above are, so they do
  not depend on the VM's clock or on code layout between builds.
- **XXH64's remaining x86 gap is Intel-only and lives between 64 and 256
  bytes**: Emerald Rapids 0.84-0.90x of cespare there, Ice Lake 0.92-0.95x,
  while Zen 3 and Zen 4 are level or ahead across that window and every core
  is exactly level from 1 KiB up. The combined-mask tail closed it on AMD
  and not on Intel -- the same split as the kernel finding above.

Which physical CPU an amd64 runner gives you is a lottery: the pool has
served Zen 3 (EPYC 7763), Zen 4 (EPYC 9V74, sometimes with AVX-512 masked
off by the host, in which case it dispatches to AVX2 and says nothing about
AVX-512) and the three Intel cores above. Dispatch `bench.yml` a few times
if a particular vendor is wanted, and read `cpu.txt` in the artifact before
trusting which core produced a number.

### The x86 XXH64 primes: registers win on Intel, lose on AMD

Holding the primes in registers rather than behind a table pointer is worth
12-16% at 32..256 bytes on a Redwood Cove, and it is a regression of the
same shape and size on AMD. Measured as 2518092 against 32598bc, one build
per side, on GitHub runners whose competitor rows moved by at most 0.1% in
the same binaries:

| bytes | Ice Lake-SP | Zen 3 |
|---|---|---|
| 4 | -9.4% | -7.3% |
| 8 | -0.2% | -13.3% |
| 16 | +4.5% | -16.6% |
| 32 | +14.6% | -8.8% |
| 64 | +13.0% | -6.4% |
| 128 | +12.7% | -5.5% |
| 256 | +7.9% | -5.2% |
| 512 | +4.2% | -3.4% |
| 1024 | +2.2% | -1.6% |
| 4096 and up | level | level |

Against cespare/xxhash that moves Ice Lake from 0.85-0.95x to 1.01-1.10x
over 32..256 bytes -- the Intel window this file lists as open, now closed --
and Zen 3 from 1.00-1.15x to 0.91-1.00x over 4..512. Both cannot be had from
one kernel: the two vendors want opposite things from the same code, the way
the two arm64 cores want opposite lane rounds. If it is worth fixing, the
precedent is there -- generate both x86 forms and pick by CPUID vendor at
init, as `pickBackend` already does for feature bits -- and the cost is a
second amd64 XXH64 kernel to verify.

**It is Zen 3's alone.** An EPYC 9V45 sampled after the change sits at
0.94-1.04x of cespare over 4..4096 bytes, and the EPYC 9V74 sampled before
it was 0.94-1.04x too, so the Zen 4-class parts are level either way and
only Zen 3 pays. That narrows any fix to a family check rather than a vendor
one -- and Zen 3 is Milan, which is most of the AMD cloud fleet, so it is
not nothing. Nothing else regressed: XXH3 is level or better at every size
on both cores, and the streaming and long paths are untouched.

**The vendor pick costs this Zen 4 a third of a short hash.** Measured on the
shipped tree against 1c7d6ea, the commit before it, three relinked layouts
each, cespare/xxhash's rows in the same binaries as the control (4.4% mean
drift, 9.4% worst):

| bytes | before | shipped | vs cespare, before -> after |
|---|---|---|---|
| 4 | 2.71 ns | 3.57 ns (+31.8%) | -10.4% -> -33.1% |
| 8 | 2.63 ns | 3.42 ns (+30.2%) | -7.7% -> -24.0% |
| 16 | 3.02 ns | 3.63 ns (+20.4%) | +1.6% -> -12.9% |
| 32..256 | | -6..+1% | -1..-4% -> +4..+6% |

Every layout agrees at 4 bytes (2.84 -> 3.76, 2.71 -> 3.52, 2.63 -> 3.55), so
it is not the caller-alignment lottery. It is not the prime form either:
forcing the register form on the same shipped tree gives back 33% at 4 bytes,
23% at 8 and 18% at 16, while leaving 32..256 alone. What costs is the
dispatch itself -- the taken branch out of `sum64Scalar` into
`sum64ScalarPtr`, which a 2.6 ns hash cannot amortize and a 6 ns one can.
Intel takes the fall-through and pays nothing, so this is AMD-only, and it is
larger than the 5-17% at 32..256 the split exists to fix.

The shape that would keep both: the two forms differ only in how the lane
loop reaches the primes, and the vendor difference was only ever measured
from 32 bytes up, so the test belongs *after* the kernel's own `n < 32`
branch rather than before it. Short inputs would then run one shared,
branch-free tail and never see the dispatch. That costs the short path
nothing and leaves the block path exactly as it is now.

**Confirmed on a Zen 4 directly**, and as the change itself rather than as a
ratio against cespare: the current tree against the same tree with only the
prime placement reverted, three relinked layouts each, cespare's rows as a
within-binary control that moved 0.6% on average. Registers read +2.3% at
32 bytes, +1.9% at 64, 0.0% at 128, +4.5% at 256 -- **+2.2% over the 32..256
window where Zen 3 reads -6.5%** -- and -1..-3% at 512 and a kibibyte, which
is where the control's own drift lives. So a desktop Zen 4 agrees with the
EPYC samples: the register form is right for it, and only Zen 3 pays. A
CPUID vendor split would put this core on the slower form to rescue that
one, so any fix wants a family check.

Two notes for whoever measures this next. The `bench/sweep` harness changed
in the same range of commits, so its before-and-after across that boundary
is meaningless -- it shows cespare 34% "faster" at 0..8 bytes because each
implementation got its own loop, and it flatters us more than cespare
because our inlinable wrapper was the one paying two calls. Use the compare
suite across builds. And the whole XXH64 effect is in the one-shot kernel:
`Digest` streaming moved 0.2% at every write size on Zen 3.

### The 33..128 inlining is not free on Zen 3

Inlining the 33..128 rungs won 26.8% at 64 bytes and 12.0% at 128 on Zen 3,
and cost 7.6% at 32 bytes, 2.1% at 512 and 5.2% at a kibibyte -- 3.74 to
4.05 ns, 18.46 to 18.86, 26.24 to 27.67. Measured across two runner
allocations before and four after, with zeebo's rows from the same binaries
unmoved to +-0.3% as the control, so it is not VM weather. The M2 shows
none of it (32 bytes -1.1%, a kibibyte unchanged), which makes it x86 code
placement rather than work: `sum64` grew, and everything laid out after it
moved with it -- the caller-alignment lottery under Benchmarking, seen from
the other side. Settle it the way that section prescribes, with three
relinked layouts on an AMD box, before trading the 64-128 byte win away.

**A second x86 core says the costs are placement and the win is real.** The
commit pair was measured in isolation on a Redwood Cove, at both alignment
phases, medians of five (win positive, as above):

| bytes | Zen 3 | Redwood Cove, phase 0 | phase 32 |
|---|---|---|---|
| 32 | −7.6% | −2.0% | +0.1% |
| 64 | +26.8% | +6.9% | +7.2% |
| 128 | +12.0% | −0.7% | −0.8% |
| 512 | −2.1% | −0.1% | +0.4% |
| 1024 | −5.2% | +0.7% | +0.5% |

Every one of the Zen 3 *costs* is gone: nothing at 32, 512 or a kibibyte
exceeds a point, and the two phases bracket zero at each of them, which is
what a placement artifact looks like when the placement is varied. The 64-byte
*win* survives on both cores and in both phases, smaller here (7% against
27%). 40 and 96 bytes, not in the Zen 3 table, win 5.6-8.6% and 2.7-2.9%.
So the rung inlining is worth keeping on its own merits, and the Zen 3 losses
are still owed the three relinked layouts on an AMD box -- this only shows
they do not follow the change onto other hardware.

### rapidhash

- **The prologue's mix is a constant when the seed is zero, and Sum64 takes
  it that way.** rapidhash opens every hash with
  `seed ^= mix(seed ^ secret[2], secret[1])`, which for an unseeded call is
  always `0x422765567d8fbfd6`. The secret table carries it as a ninth word
  and the generator emits a second kernel, `sum64RapidNS`, whose prologue
  loads it instead of multiplying; `Sum64` and `Sum64String` reach that one
  and `Sum64Seed` the original, so the choice is the entry point's and costs
  no branch. It removes a multiply, and a serial one that stands at the head
  of every hash before any input is read. Measured on a Zen 4, three
  relinked layouts: **+6.0% at 1..16 bytes, +2.1% at 17..32, +14.7% at
  33..112, +11.5% at 113..224, +3.6% at 225..1024** and nothing from 4 KiB
  up, where one multiply is lost in the block loop. The portable path gains
  far more -- 46% at 4 bytes, 41% at 17..32 -- because the kernel was
  already overlapping the multiply that the Go code could not.
- **mulx does not help this kernel on Zen 4, and the evidence is now in.**
  The x86 face notes that `mulq`'s fixed rdx:rax pair costs a move per lane
  round and that BMI2's `mulx` would lift it. It does, and the loop is
  slower for it: a seven-lane round measures 13.57 cycles with `mulq` and
  15.58 with `mulx` on this core, and raw throughput is 1.57 cycles per
  multiply against 1.62. Seven multiplies at 1.57 is 11 cycles, so the
  shipped loop at 13.57 is within 23% of the multiplier bound with the
  instruction count doing the rest. No second kernel, no CPUID check.
- **The short path is a call, not arithmetic.** An empty kernel call with
  this signature is 1.28 ns on a Zen 4 against 2.01 for a whole 8-byte hash,
  so under a nanosecond of it is the hash. Moving the 0..16 case into Go so
  it inlines into the caller would remove the call -- and does not fit: the
  case costs 190 inliner nodes against a budget of 80, and even the 8..16
  branch alone costs 106, which takes `Sum64` itself from inlinable to a
  call at 148. Tried, measured, reverted.
- **224 bytes is slower than 225** (8.56 against 7.50 ns), because 224 does
  not enter the 224-byte block loop: it runs one 112-byte block of seven
  parallel lanes and then all six ladder rungs, which are serial, where 225
  runs fourteen parallel lanes and no rungs. Wire format, not a bug.

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
- The 129..240 ladders' tails are unrolled into a chain of length tests, one
  per round the reference's loop would have run, in the loop's own order --
  Go does not unroll loops, so the loop form recomputed `16*i` per round and
  read the bound each time. Worth 27% of a 240-byte hash, rising with length
  from nothing at 144; it is what removed the inversion where 240 bytes cost
  more than 256. The 128-bit ladders' fixed four-round prologues are written
  out for the same reason. In the seed-free 128-bit prologue the eight cross
  terms are computed inside their rounds, not hoisted to the top: hoisting
  extends eight loads' live ranges across the whole prologue and measured
  6-7% slower.
- The 17..32 rung of the seed-free ladders is inlined into `sum64NS` and
  `sum128NS`: two mixes do not amortize a call the way four or more do, and
  at that length the call was 7% -- the last size where zeebo/xxh3 was ahead.
  The longer rungs keep the call; inlining them all was not measured to pay
  and would bloat a nosplit function.
- **Measured and rejected**: a `prefetcht0` a block ahead in the fast loop
  (neutral at 64 KiB, slightly negative at 1 MiB); a second pair of accumulator
  chains (the loop is throughput-bound, not latency-bound); mixing the five-
  and six-instruction stripe forms; an `and`-plus-`add` scramble on AVX2 that
  trades one operation per block for a mask register materialized on every
  call; a copy of both 64-bit ladders with the default secret compiled in as
  immediates, worth 1-3% -- a 64-bit immediate costs about what an L1 load
  costs, so this is far less than it looks.

The below-256-byte gap to zeebo/xxh3 is closed (2026-08, go1.26.5). Two
things closed it. The toolchain moved first: where this file recorded the two
executing the same instruction count at 128 bytes with an unexplained IPC gap
(4.7 against 5.5), go1.26.5 compiles the 64-bit path to ~18% fewer
instructions at the same lower IPC, and the two measured dead level before
any code changed. The rest was the "one more call level" hypothesis,
confirmed by deleting it:

- **The 33..128 rungs are hand-inlined into sum64NS and sum128NS** rather
  than called. The call's true cost was far more than call+ret -- the callee
  re-tested the length tree and the boundary blocked load hoisting -- and
  removing it measured 64-bit 3.35 -> 3.04 ns at 64 bytes and 5.27 -> 5.03 at
  128; 128-bit 5.13 -> 4.53 and 8.50 -> 6.66, from -7% and -17% behind zeebo
  to +5% ahead. The 129..240 rungs keep the call on purpose: they were
  already ahead, and their bodies would bloat a nosplit function. The ladder
  functions themselves remain for the seeded digest path.
- **The 128-bit rounds run hi-half first, j-side loads first** (zeebo's
  statement order), worth 3-4% on its own before the inlining: the function
  is dense in multiplies contending for the one integer-multiply port, and
  which half finishes last gates the merge's imuls.
- **The seeded one-shots have full-bodied twins**: sum64Seeded/sum128Seeded
  carry the short cases and the 17..128 rungs inline, so Sum64Seed reaches
  the arithmetic in one call instead of two-plus-re-dispatch. Seeded 8 B went
  3.29 -> 2.33 ns (level with zeebo, was -36%), 64 B 4.88 -> 3.50 (+7%
  ahead). The >240 derive branch lives in sum64SeededLong because the
  192-byte secret frame does not fit the nosplit budget once the race
  detector inflates it. sum64/sum128 (secret-parameterized) remain for the
  digest.

After those, a fresh per-length sweep on this core has both widths level or
ahead of zeebo at every class: 0..3 bytes included (the old 3-8% deficit
there is gone under go1.26.5), and the 128-bit 33..128 zone that briefly
measured -5..-9% behind is +5..+6% ahead direct-call.

### amd64, measured on Redwood Cove (Core Ultra 9 185H, Meteor Lake)

Six P-cores at 4.8-5.1 GHz and no AVX-512, so **AVX2 is what dispatch picks
here** and the AVX-512 kernel goes unexercised except through the simulator.
`perf` works; pin to a P-core (0-11; 12-21 are Crestmont E-cores at 2.5-3.8
GHz and will quietly halve any number taken without `taskset`).

The core has three 256-bit vector ALU ports, so the AVX2 stripe's twelve
instructions want four cycles. It gets **4.42 cycles per stripe** at 16 KiB,
about 90% of that bound, and 4.86-4.92 out of L2 at 64 KiB and beyond.
Nothing in the stripe is left to remove -- the ten 256-bit ALU operations are
the algorithm's floor over 512 bits -- so this backend is finished on this
core barring a wider one.

- **The accumulator path's fixed cost is 30.7 cycles**, from a fit over
  256..2048 bytes (`cycles = 30.7 + 4.32 x stripes`). At 256 bytes that is
  64% of the hash. Where it goes, by profile: the kernel 40%, `mergeAccs`
  28%, `sum64NS`'s own dispatch and the `initAcc` copy 16%, and the
  `hashLong` wrapper 9%.
- **The `hashLong` wrapper is worth 4.5% at 256..1024 bytes and is not
  taken.** It is a three-way switch over `backend`, which costs 199 nodes
  against the inliner's budget of 80, so it stays a real call and the kernel
  is two calls from `sum64NS` where it could be one. Collapsing it to a
  single call -- verified by hardcoding `hashLongAVX2`, which does inline at
  cost 64 -- takes 256 bytes from 47.5 to 45.4 cycles, 512 from 65.5 to 62.6,
  1024 from 99.1 to 94.6, and nothing at 4 KiB where the kernel hides it.
  Three ways to get it, all rejected: a func variable is one call and would
  inline, but escape analysis cannot see through it and would put `acc` on
  the heap; splitting the rare backends behind a second function is still two
  calls and 125 nodes; duplicating the switch into a fused long-path function
  per architecture means six copies of the convergence. Worth revisiting if
  the inliner's treatment of calls changes.
- **The three changes this core motivated, each re-measured on the merged
  tree at both alignment phases.** Every one was taken as before/after
  binaries of the same commit pair, medians of five, and repeated with a
  live `//go:noinline` pad ahead of `main.main` to move it from phase 32 to
  phase 0. Go aligns functions to 32 bytes, so those two *are* the lottery's
  modes; a result that holds in both is not a layout draw.

  Primes in registers rather than through a table pointer, cycles per hash:

  | bytes | phase 32 | phase 0 |
  |---|---|---|
  | 16 | −6.0% | −6.3% |
  | 32 | −14.6% | −14.2% |
  | 64 | −14.5% | −13.7% |
  | 128 | −16.0% | −15.7% |
  | 240 | −8.0% | −8.3% |
  | 256 | −9.7% | −9.5% |
  | 512 | −5.3% | −5.3% |
  | 1024 | −3.0% | −2.6% |

  The two phases agree to within 0.6 points at every length. At 4 and 8
  bytes they do not agree and the sign flips (+0.7%/−2.5%, +0.2%/−1.9%),
  which is the lottery and not a result either way -- the kernel does not
  enter its block loop there.

  The convergence written out in `sum128NS` (removing two `mergeAccs` calls;
  287 nodes, never inlinable): −8.7%/−8.6% at 256 bytes, −4.9%/−5.1% at a
  kibibyte, −2.2%/−2.7% at 4 KiB. The same change in `sum64NS` removes one
  call and measures neutral -- −0.2%/−1.5% at 256, −0.6%/−0.4% at 1024 --
  and is written that way for symmetry, not because it pays.

  Staging a block instead of half of one, cycles per MiB, measured with the
  small-write drain in place: −6.4% at 16-byte writes, −14.1% at 64, −13.5%
  at 256, −0.5% at a kibibyte, −1.4% at 4 KiB. The drain's `len(p) <
  internalBufferSize-63` gate now admits writes up to 961 bytes rather than
  449, and that costs nothing here; it has not been re-checked on the Zen 4
  it was tuned on.
- Against the reference implementations, `bench/compare_test.go`, median of
  six, pinned: XXH3-64 is ahead of zeebo/xxh3 at every size except 16 bytes
  (−6%), by +17% at 64, +20% at 128, +36% at 256 and +5-12% beyond; XXH3-128
  is ahead everywhere from 32 bytes (+7% at 32, +14% at 128, +21% at 256,
  +5-14% beyond) and level at 16; XXH64 is within +0 to +3% of
  cespare/xxhash at every size. Streaming a mebibyte: +16% at 16-byte
  writes, +1% at 64, −2% at 256, +17% at 1 KiB, +66% at 4 KiB.

  The residual at 64- and 256-byte writes is call depth: `Write` -> `write`
  -> `absorb` -> `accumBlocks` -> kernel against zeebo's `Write` ->
  `updateString` -> kernel, which it reaches by putting its backend dispatch
  inline as a chain of `if hasAVX2` on package-level bools instead of behind
  a wrapper. The same inliner budget that blocks the `hashLong` fix blocks
  this one.
- **XXH3-128 of an empty input costs 9.5 cycles against zeebo's 6.0**, 62
  instructions against 38, because zeebo compiles the default secret's
  bitflips in as constants and haste loads them through the secret pointer.
  Fixing it needs the specialization that was already measured and rejected
  above -- the guard compares cost more than the L1-hot loads they remove --
  so it stands.

### The other Go rapidhash

[vkudryk/rapidhash-go](https://github.com/vkudryk/rapidhash-go) implements the
**original** rapidhash, not the one in `rapidhash/`. Its secret is three words
where the current algorithm's is eight, it folds the length into the prologue
rather than the final mix, and its default seed is nonzero rather than zero.
The two disagree at every length, zero included; the reference C agrees with
this package at all of them.

The first three secret words are byte-identical between the two, which is what
makes them easy to confuse and worth writing down. It was briefly added to
`bench/` and then removed: benchmarking two different algorithms beside each
other reads as a like-for-like comparison however the rows are labelled, and
it forced `bench/go.mod` from Go 1.22 to 1.24.2 for the privilege.

## Reference vectors

`vectors_test.go` and `xxh64/vectors_test.go` are generated by `ref/gen.c`
against the xxHash v0.8.3 C source (the second with the argument `xxh64`); the
header of that file has the commands. Do not hand-edit the vectors. If a
change makes them fail, the change is wrong — they are the definition.
