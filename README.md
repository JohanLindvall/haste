# xxhaste

[![CI](https://github.com/JohanLindvall/xxhaste/actions/workflows/ci.yml/badge.svg)](https://github.com/JohanLindvall/xxhaste/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/JohanLindvall/xxhaste.svg)](https://pkg.go.dev/github.com/JohanLindvall/xxhaste)

XXH3 for Go — the 64-bit and 128-bit hash from [xxHash](https://github.com/Cyan4973/xxHash)
v0.8.3, with generated assembly kernels for **SSE2, AVX2, AVX-512, NEON and SVE2**.

Output is bit-identical to the reference C implementation on every path,
including seeds and custom secrets. No dependencies outside the standard
library.

```
go get github.com/JohanLindvall/xxhaste
```

## Use

```go
h := xxhaste.Sum64(data)              // 64-bit
h := xxhaste.Sum64String(key)         // no copy, no allocation
c := xxhaste.Sum128(data)             // 128-bit: c.Lo, c.Hi, c.Bytes()

h := xxhaste.Sum64Seed(data, seed)    // keyed by a seed
h := xxhaste.Sum64Secret(data, sec)   // keyed by a custom secret

d := xxhaste.New()                    // streaming; implements hash.Hash64
d.Write(chunk)
h, c := d.Sum64(), d.Sum128()         // both widths from one pass

h := xxhaste.Sum64Uint64(key)         // fixed-size keys: no call at all
h := xxhaste.Sum64Uint64Seed(key, s)
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

| size | xxhaste | zeebo/xxh3 | cespare (XXH64) |
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

| size | xxhaste | zeebo/xxh3 | cespare (XXH64) |
|-----:|--------:|-----------:|----------------:|
| 4 | **2.13** | 2.32 | 2.45 |
| 8 | 2.17 | 2.22 | 2.46 |
| 16 | 1.99 | 2.00 | 3.12 |
| 32 | **2.34** | 2.41 | 6.11 |
| 64 | 3.55 | 3.52 | 8.16 |
| 128 | 5.53 | 5.47 | 11.6 |
| 240 | **9.38** | 10.6 | 18.0 |
| 256 | **9.70** | 13.8 | 18.6 |
| 512 | **11.5** | 16.4 | 32.0 |
| 1 Ki | **16.1** | 21.6 | 57.9 |
| 4 Ki | **47.2** | 53.1 | 222 |
| 16 Ki | **163** | 191 | 873 |
| 64 Ki | **645** | 756 | 3468 |
| 1 Mi | **11144** | 12751 | 55008 |

**102 GB/s** at 64 KiB against 87, and 94 GB/s at a mebibyte. Streaming one in
4 KiB pieces: **63.8 GB/s** against 35.7 and 18.7.

Below 256 bytes the two are now level or better at every size. What closed
the gap was never one thing: seed-free
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
| portable Go | other architectures, or `-tags purego` | executed natively |

`xxhaste.Backend()` reports which one is live.

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
  length is not a multiple of the secret consume rate.
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
- **Cross-implementation**: results are compared against zeebo/xxh3, an
  independent port, in `bench/`.

All of it runs in CI on every push and pull request, on amd64 and on arm64
hardware, against the current Go and the 1.21 that `go.mod` declares: the suite
under four build configurations, the SSE2 and AVX2 kernels forced under qemu, a
minute on each fuzz target, a test-binary build for eleven architectures, and a
check that the committed assembly still matches what the generator emits.

## Releases

Versions are cut automatically: a push to `main` that touches `.go`, `.s` or
`go.mod` tags the next patch version once the whole matrix above is green.
Documentation-only pushes are not tagged.

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
