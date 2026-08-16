# haste

[![CI](https://github.com/JohanLindvall/haste/actions/workflows/ci.yml/badge.svg)](https://github.com/JohanLindvall/haste/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/JohanLindvall/haste.svg)](https://pkg.go.dev/github.com/JohanLindvall/haste)

Three fast non-cryptographic hashes for Go, one package each, with assembly
kernels that are generated rather than written:

| package | hash | kernels |
|---|---|---|
| `xxh3` | XXH3, 64- and 128-bit, one-shot and streaming | SSE2, AVX2, AVX-512, NEON, SVE2 |
| [`xxh64`](#xxh64) | XXH64, the older scalar member of the family | amd64, arm64 |
| [`rapidhash`](#rapidhash) | rapidhash, wyhash's successor | amd64, arm64 |

Each is bit-identical to its reference C implementation on every path — seeds
and custom secrets included — and is checked against vectors taken from that
code. No dependencies outside the standard library.

```
go get github.com/JohanLindvall/haste
```

Most of this README is about `xxh3`, which is the one to reach for unless you
need to match something. It was at the module root until it moved to `xxh3/`:
an import of the root now needs `/xxh3` appended, and the identifiers are
unchanged.

## Use

```go
import "github.com/JohanLindvall/haste/xxh3"

h := xxh3.Sum64(data)              // 64-bit
h := xxh3.Sum64String(key)         // no copy, no allocation
c := xxh3.Sum128(data)             // 128-bit: c.Lo, c.Hi, c.Bytes()

h := xxh3.Sum64Seed(data, seed)    // keyed by a seed
h := xxh3.Sum64Secret(data, sec)   // keyed by a custom secret

d := xxh3.New()                    // streaming; implements hash.Hash64
d.Write(chunk)
h, c := d.Sum64(), d.Sum128()         // both widths from one pass

h := xxh3.Sum64Uint64(key)         // fixed-size keys: no call at all
h := xxh3.Sum64Uint64Seed(key, s)
```

For a key whose size is known at compile time there is nothing to switch on
and no secret to load, so `Sum64Uint32`, `Sum64Uint64` and `Sum64Uint128` fold
into the caller entirely. They return exactly what `Sum64` returns for the same
bytes in little-endian order, and cost about half as much:

| | fixed-size | `Sum64` |
|---|---:|---:|
| 4 bytes | **1.89 ns** | 3.04 |
| 8 bytes | **1.86 ns** | 3.01 |
| 16 bytes | **2.42 ns** | 5.74 |

Seeded forms cost two instructions more, which is cheap enough to key a hash
table per instance rather than per program.

`Sum64Seed` derives a 192-byte secret per call for inputs over 240 bytes, which
is what XXH3 defines a seeded long hash to be. Hashing many long inputs under
one seed? Use `NewSeed`, which derives it once.

## Speed

Against [zeebo/xxh3](https://github.com/zeebo/xxh3) (the fastest existing Go XXH3) and
[cespare/xxhash](https://github.com/cespare/xxhash) (XXH64 — a different, cheaper
algorithm, included for scale). Nanoseconds per hash, lower is better.

### arm64

Azure Cobalt 100 (Neoverse N2, 3.4 GHz), Go 1.26:

| size | haste | zeebo/xxh3 | cespare (XXH64) |
|-----:|--------:|-----------:|----------------:|
| 4 | 3.04 | 2.97 | 3.28 |
| 8 | 3.04 | 2.97 | 3.50 |
| 16 | **3.25** | 3.25 | 4.25 |
| 32 | **4.02** | 4.66 | 7.65 |
| 64 | **5.67** | 7.27 | 9.08 |
| 128 | **8.94** | 12.74 | 12.49 |
| 240 | 19.97 | 23.25 | 19.68 |
| 256 | 20.15 | 26.31 | 18.76 |
| 512 | **28.09** | 44.81 | 30.02 |
| 1 Ki | **43.76** | 83.13 | 49.63 |
| 4 Ki | **152** | 327 | 163 |
| 16 Ki | **582** | 1308 | 620 |
| 64 Ki | **2323** | 5236 | 2472 |
| 1 Mi | **40153** | 84858 | 44856 |

That is **28 GB/s** sustained against 12.5, ahead of the other XXH3 port at
every size from 32 bytes up — by 28% at 64 bytes and 43% at 128 — and ahead of
cespare's XXH64, a structurally cheaper algorithm, everywhere except a narrow
window around 240–256 bytes where it holds a few percent. Below 32 bytes it is
a tie within 3%, where more than half the cost is Go's call overhead rather
than hashing; that is what the fixed-size entry points above are for.

The 128-bit hash tracks the same shape: level below 32 bytes, ahead from 64
bytes up, 2.25x at 64 KiB.

Streaming a mebibyte through `Digest.Write` is ahead of the other XXH3 port at
every piece size: 6.8 GB/s at 64-byte writes against 5.5, 14.5 at 1 KiB
against 8.5, 19.5 at 4 KiB against 8.7. Tiny writes are memmove-bound for
everyone - at 16 bytes all three implementations sit within 13% of each other,
because the cost is staging fragments, not hashing them.

Two things get it there. The first is restructuring the accumulator rather
than widening it. XXH3 specifies

```
acc[i]   += mul32(data[i] ^ secret[i])
acc[i^1] += data[i]
```

The lane swap in the second line forces a shuffle per stripe. Keeping two
accumulators — one for the products, one for the raw data — and folding
`acc = accA + swap(accB)` only when the value is actually needed (once per
1 KiB block, not once per 64 bytes) removes it: five vector operations per
register per stripe instead of the reference's six, and two independent
dependency chains instead of one. Worth 17% here, and llvm-mca puts it at 17%
on Skylake-AVX512 and 22% on Zen 4.

The second applies only to cores whose vector pipes are scarce next to their
integer pipes, which is why it is gated on identifying the core: half of each
stripe moves into general-purpose registers, where the lane swap costs nothing
at all, because the neighbouring lane is just a different register name. That
half also stops reloading the secret — it advances one 64-bit word per stripe,
so all but one of the words a stripe needs are already in registers. On a
Neoverse N2 the split kernel is a further 40%.

### amd64

Ryzen 7 8840HS (Zen 4), Go 1.26, AVX-512. Same benchmark, same three
implementations:

| size | haste | zeebo/xxh3 | cespare (XXH64) |
|-----:|--------:|-----------:|----------------:|
| 4 | **2.19** | 2.39 | 2.46 |
| 8 | **2.17** | 2.39 | 2.46 |
| 16 | 1.99 | 1.98 | 3.10 |
| 32 | **2.42** | 2.47 | 6.04 |
| 64 | **3.19** | 3.54 | 8.00 |
| 128 | **5.18** | 5.45 | 11.4 |
| 240 | **9.34** | 10.7 | 17.7 |
| 256 | **9.28** | 13.5 | 18.0 |
| 512 | **11.3** | 16.2 | 31.2 |
| 1 Ki | **15.7** | 21.8 | 57.6 |
| 4 Ki | **46.9** | 52.3 | 217 |
| 16 Ki | **160** | 186 | 847 |
| 64 Ki | **635** | 741 | 3418 |
| 1 Mi | **11248** | 12632 | 55476 |

**103 GB/s** at 64 KiB against 88, and 93 GB/s at a mebibyte. Streaming one in
4 KiB pieces: **64.7 GB/s** against 35.8 and 18.7. Ahead of the other XXH3
port at every size except 16 bytes, where the two are within a percent.

The same benchmark on a Core Ultra 9 185H (Redwood Cove P-core, Meteor Lake),
which has **no AVX-512** and therefore exercises the AVX2 kernel:

| size | haste | zeebo/xxh3 | cespare (XXH64) |
|-----:|--------:|-----------:|----------------:|
| 4 | **1.61** | 1.77 | 1.91 |
| 8 | **1.69** | 1.77 | 2.02 |
| 16 | 1.68 | 1.58 | 2.35 |
| 32 | **2.00** | 2.10 | 4.64 |
| 64 | **2.68** | 3.13 | 6.21 |
| 128 | **4.23** | 5.10 | 9.56 |
| 240 | **8.67** | 9.67 | 15.75 |
| 256 | **9.87** | 13.4 | 16.2 |
| 512 | **13.5** | 16.1 | 29.3 |
| 1 Ki | **20.3** | 22.6 | 56.3 |
| 4 Ki | **64.4** | 70.9 | 217 |
| 16 Ki | **237** | 254 | 838 |
| 64 Ki | **1044** | 1108 | 3341 |
| 1 Mi | **16586** | 17492 | 53677 |

**63.2 GB/s** at a mebibyte against 59.9, and ahead at every length except
16 bytes, where the other port compiles the default secret's keying in as
constants and this one loads it — the price of supporting a custom secret on
the same path. The 128-bit hash is ahead from 32 bytes up: +14% at 128, +21%
at 256, +14% at a kibibyte. Streaming a mebibyte: 4.4 GB/s at 16-byte writes
against 3.8, 34.5 at 1 KiB against 29.5, **52.4 at 4 KiB against 31.5**; at
64 and 256 bytes the two are within 2% of each other, where the staging path
is most of the cost.

Below 256 bytes the two are now level or better at every length from 4 up --
an exhaustive per-length sweep (bench/sweep) puts only 0..3 bytes more than
3% behind, about 0.3ns of the signature cost that custom-secret support
carries. What closed the gap was never one thing: seed-free
twins of every path for the unseeded entry points, calling the ladders without
an intermediate function, and unrolling the 129..240 ladder's tail -- Go does
not unroll loops, and the reference's tail loop was recomputing offsets and
carrying its accumulator serially where the unrolled form runs every offset as
an immediate. That last one is 27% of a 240-byte hash, and it is why the
scalar ladder no longer loses to its own SIMD path at the 240/241 boundary.
The final size to fall was 32 bytes, whose two-mix rung now sits inline in
the dispatcher: at two and a half nanoseconds, the call to the ladder was
the whole deficit.

What the AVX-512 kernel does that the others do not: it holds the whole 1 KiB
block's secret schedule in the upper sixteen registers, which have no ABI
meaning on amd64, so each stripe's only memory reference is the input; and it
collapses the between-block scramble to three dependent steps, using ternary
logic to fold an xorshift and a secret xor into one instruction and a 64-bit
lane multiply for the rest.

Everything from 256 bytes up also gained from something duller. The
accumulators travel between Go and the kernels through memory, Go writes them
in 16-byte moves, and a 64-byte load spanning four of those stores has to wait
for them to reach the cache instead of taking them from the store queue.
Reading them back in the width they were written is worth a quarter of a
256-byte hash — more than any of the kernel work at that length, and it is why
even the untouched SSE2 kernel got faster.


## XXH64

The subpackage `xxh64` is XXH64 — the older, scalar 64-bit hash of the family,
and what most Go code that says "xxhash" computes today. Its API mirrors
cespare/xxhash, so switching is an import path:

```go
import "github.com/JohanLindvall/haste/xxh64"

h := xxh64.Sum64(data)            // also Sum64String, Sum64Seed, Sum64SeedString
d := xxh64.New()                  // hash.Hash64; NewSeed(seed) for a keyed one
d.Write(chunk)
h = d.Sum64()
```

The whole hash of any input is one call into a generated kernel — the same
generator, simulator, C-derived vectors and fuzz targets as XXH3 — and there
is a portable implementation for everything else. XXH64's lane loop is bound
by each lane's multiply-rotate-multiply chain and by the integer multiplier,
so a kernel cannot beat the algorithm; what it can do is not lose anything to
call layering or codegen. On the arm64 side there are two forms of the lane
round, chosen by core: the fused multiply-add that Neoverse cores run at
their chain bound, and a split multiply and add for Apple's, where a fused
multiply-accumulate waits the whole multiplier latency for an addend that
comes from a plain multiply. Measured on an Apple M2 P-core against
cespare/xxhash (which is hand-written assembly on both amd64 and arm64):

| size | xxh64 | cespare | | size | xxh64 | cespare |
|-----:|------:|--------:|-|-----:|------:|--------:|
| 4 | 2.29 | 2.30 | | 256 | **14.5** | 15.9 |
| 8 | 2.31 | 2.31 | | 1 Ki | **50.1** | 65.3 |
| 16 | 2.59 | 2.58 | | 4 Ki | **196** | 259 |
| 32 | 4.62 | 4.43 | | 16 Ki | **785** | 1030 |
| 64 | 6.02 | 5.92 | | 64 Ki | **3132** | 4117 |
| 128 | **8.88** | 9.20 | | 1 Mi | **50103** | 65814 |

Nanoseconds per hash: level through 16 bytes, within 4% at 32 and 64, ahead
from 128 up and **31% ahead from a kibibyte** (20.9 GB/s against 15.9).

On amd64 the kernel is the same shape as cespare's, imul-bound at eight
multiplies per 32-byte block, and measures level with it — on a Core Ultra 9
185H, within 0 to 3% at every size:

| size | xxh64 | cespare | | size | xxh64 | cespare |
|-----:|------:|--------:|-|-----:|------:|--------:|
| 4 | 1.88 | 1.91 | | 256 | 16.1 | 16.2 |
| 8 | **1.95** | 2.02 | | 1 Ki | **55.3** | 56.3 |
| 16 | **2.30** | 2.35 | | 4 Ki | **211** | 217 |
| 32 | 4.59 | 4.64 | | 16 Ki | 827 | 838 |
| 64 | 6.18 | 6.21 | | 64 Ki | 3320 | 3341 |
| 128 | 9.55 | 9.56 | | 1 Mi | 53431 | 53677 |

Two changes got it there and neither was in the lane loop. The tail opens
with combined-mask skips, so a trivial tail pays one taken branch instead of
five. And the kernel used to reach its primes through a pointer to a table:
on this core that costs 12-16% over 32..128 bytes, so it holds them in
registers instead — measured at both of the two code-alignment phases the
linker can produce, which agree to within 0.6 points. See CLAUDE.md; the
effect is reproducible, absent on Zen 4, and unexplained, which is worth
reading before touching that prologue.

## rapidhash

[rapidhash](https://github.com/Nicoshev/rapidhash) is a third algorithm again,
and the reason it is here is that it is not shaped like the other two: no
vector unit at all, just the low and high halves of 64x64 multiplies folded
together, over seven independent lanes. It wins where the multiplier is idle
and the vector pipes are busy or absent, which is a different machine from the
one XXH3 is tuned for.

```go
import "github.com/JohanLindvall/haste/rapidhash"

h := rapidhash.Sum64(data)
h := rapidhash.Sum64Seed(data, seed)
```

There is no streaming form. rapidhash reads the tail of its input before it has
finished with the head and needs the length before it starts, so there is
nothing to feed in pieces; use `xxh3.Digest` when input arrives incrementally.

It is bit-identical to the reference C, over 640 vectors generated from it by
`ref/rapidgen.c`, and both kernels are generated the same way the other two
packages' are.

**If you are comparing against another Go rapidhash, check which version it
is.** [vkudryk/rapidhash-go](https://github.com/vkudryk/rapidhash-go)
implements the *original* rapidhash: three secret words where the current
algorithm has eight, the length folded into the prologue rather than the final
mix, and a nonzero default seed. It produces different hashes from this
package at every length, including zero — verified against the reference C,
which agrees with this package throughout. Neither is wrong; they are
different versions of the algorithm, and the first three secret words are
identical, which is what makes the two easy to mistake for each other. It is
not in `bench/`, because timing two different algorithms side by side invites
exactly that mistake.

The kernel is worth **32-36% over 225 bytes and up** against the portable Go,
on a Zen 4 — and nothing measurable below that, where the cost is the call
rather than the hashing. That is the opposite of what the other two hashes
gain from assembly, and the reason is the seven lanes: Go will not keep them
all in registers across the block loop, and the kernel does.

On amd64 the block loop has a second form, taken on Intel cores with BMI2.
`mulx` has no fixed destination, which frees the register `mulq` occupies;
that register then holds a lane's secret word across both of the lane's
rounds, halving the loop's secret loads — which is where the win is, rather
than in `mulx` itself. The choice is made inside the kernel, and only by
inputs long enough to run the loop twice, so shorter ones never execute the
test. Measured on a Core Ultra 9 185H: **6-14% from 449 bytes up**, level
between 320 and 448. It is gated to Intel because a Zen 4 measures `mulx`
alone slower than `mulq` and the paired form has not been measured there.

Short inputs are about 3% quicker for an unrelated reason: their
`in + n - k` reads are one addressing mode now rather than three
instructions.

## Backends

| backend | selected when | verified by |
|---|---|---|
| AVX-512 | CPUID AVX512F + AVX512DQ + OS ZMM state | simulation |
| AVX2 | CPUID AVX2 + OS YMM state | executed under qemu |
| SSE2 | always available on amd64 | executed under qemu |
| SVE2 | HWCAP2_SVE2, vector length 256 or 512 | simulation |
| NEON hybrid | MIDR names a core it is faster on | executed natively |
| NEON | always available on arm64 | executed natively |
| SVE2 (VL 128) | generated for verification; not selected | executed natively |
| NEON two-lane split | generated for measurement; not selected | executed natively |
| portable Go | other architectures, or `-tags purego` | executed natively |
| XXH64 scalar, amd64 | always available on amd64 | executed natively and under qemu |
| XXH64 scalar, arm64 | always; the lane round's form by core (`muladd` on Apple, `madd` elsewhere) | executed natively, both forms |

`xxh3.Backend()` and `xxh64.Backend()` report which one is live.

SVE2 is generated once per vector length: a stripe is a fixed 64 bytes, and how
many registers that occupies is exactly what SVE leaves unspecified. At 128
bits it has no width advantage over NEON, so it is not selected — but it is
still generated, because it is the only SVE2 kernel this machine can execute,
which is what makes the 256- and 512-bit ones credible. On a Neoverse V2,
llvm-mca puts SVE2 at 256 bits at twice NEON's rate.

The hybrid kernel is enabled only where MIDR_EL1 names a core it has been
shown to help. The register that identifies the core cannot be misread, only
unrecognised, so an unknown core simply keeps the kernel it had.

## Correctness

- **1512 reference vectors** generated from xxHash v0.8.3 C code: 328 input
  lengths under four seeds, and ten custom secret sizes including ones whose
  length is not a multiple of the secret consume rate. XXH64 has its own 1282,
  from the same generator over the same lengths and seeds.
- **Every backend** is checked against those vectors, not just the one this
  machine dispatches to. The ones this machine cannot execute go through the
  simulator instead.
- **Kernels against portable Go**: the three entry points are called as they
  are linked into the binary and compared with `generic.go` in the same
  process, from every starting position within a block and at nine secret
  sizes. This is the only check that reaches the streaming kernel under a
  secret whose limit is not a multiple of the consume rate — the reference
  vectors are one-shot, so a custom secret there never gets that far.
- **Streaming equals one-shot** at every length up to 1300 across nine
  chunkings — crossing the stripe, the staging area and the first block many
  times over — at nine secret sizes, plus randomized chunking.
- **Fuzzing** over input, seed, secret, chunk size, backend and marshalled
  state: `go test -fuzz FuzzStreamingMatchesOneShot`.
- **Cross-implementation**: results are compared against zeebo/xxh3 and, for
  XXH64, cespare/xxhash -- independent ports -- in `bench/`. Where a C
  compiler is present, the reference C implementation itself is compiled in
  through cgo (vendored `xxhash.h`, pinned to v0.8.3, the revision the
  vectors came from), checked for bit-identity, and benchmarked alongside;
  `bench/mdtable` turns any benchmark run into a markdown table.

All of it runs in CI on every push and pull request, on amd64 and on arm64
hardware, against the current Go and the 1.21 that `go.mod` declares: the suite
under four build configurations, the SSE2 and AVX2 kernels forced under qemu, a
minute on each fuzz target, a test-binary build for eleven architectures, and a
check that the committed assembly still matches what the generator emits. The
`xxh64` package goes through the same matrix.

## Releases

Versions are cut automatically: once the whole matrix above is green on `main`,
the next patch version is tagged if any `.go`, `.s` or `go.mod` file has
changed since the last tag. Measuring from the tag rather than from the push is
what makes it self-healing — rapid pushes supersede each other's queued CI
runs, and a superseded run never tags, so its commit is picked up by the next
release instead of being stranded. Documentation-only work is never tagged.

The module is `v0.x` while the API is still moving — `Sum64Uint64` and the
other fixed-size entry points are recent — so a minor bump may break source
compatibility. Bump the minor or major by hand (`git tag v0.2.0 && git push
origin v0.2.0`) and the automation continues from there.

## Building the assembly

The kernels are not hand-written. `internal/asmgen` describes the algorithm
once, against an interface each ISA implements, and emits GNU assembler source;
the system assembler produces the machine code, which is emitted back as
`BYTE`/`WORD` directives with the disassembly of those exact bytes as a comment
on every line:

```
WORD $0x2eb58280 // umlal v0.2d, v20.2s, v21.2s
```

This is what makes AVX-512 and SVE2 possible at all: Go's assembler knows
neither the SVE encodings nor every EVEX form.

```
go generate ./...       # needs binutils for the targets you regenerate
```

Each emitted instruction also carries its semantics as a Go closure, so the
same instruction stream that becomes machine code can be *executed by a
simulator* in the test suite. That is how AVX-512 and wide SVE2 are verified on
a machine that can run neither. `TestGeneratedFilesUpToDate` fails if the
checked-in assembly drifts from the generator.

## License

MIT, same as the original xxHash. See LICENSE.
