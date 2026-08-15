# xxhaste

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
```

`Sum64Seed` derives a 192-byte secret per call for inputs over 240 bytes, which
is what XXH3 defines a seeded long hash to be. Hashing many long inputs under
one seed? Use `NewSeed`, which derives it once.

## Speed

Measured on an Azure Cobalt 100 (Neoverse N2, 3.4 GHz), Go 1.26, against
[zeebo/xxh3](https://github.com/zeebo/xxh3) (the fastest existing Go XXH3) and
[cespare/xxhash](https://github.com/cespare/xxhash) (XXH64 — a different, cheaper
algorithm, included for scale). Nanoseconds per hash, lower is better:

| size | xxhaste | zeebo/xxh3 | cespare (XXH64) |
|-----:|--------:|-----------:|----------------:|
| 4 | 3.01 | 2.92 | 3.19 |
| 8 | 3.03 | 2.92 | 3.50 |
| 16 | 3.28 | 3.17 | 4.25 |
| 32 | 4.90 | 4.65 | 7.65 |
| 64 | **6.75** | 7.26 | 9.06 |
| 128 | **10.63** | 12.71 | 12.47 |
| 240 | **22.66** | 23.23 | 19.68 |
| 256 | **23.22** | 26.15 | 18.72 |
| 1 Ki | **54.3** | 82.1 | 49.6 |
| 16 Ki | **816** | 1301 | 620 |
| 64 Ki | **3266** | 5219 | 2475 |
| 1 Mi | **54432** | 84400 | 45515 |

That is **20.1 GB/s** sustained versus 12.6 GB/s, a 1.6× speedup on long
inputs, and a small deficit (3–5%) below 32 bytes. The 128-bit hash tracks the
same shape: 1.6× at 64 KiB, ahead from 64 bytes up, behind by 7% at 240.

Streaming a mebibyte through `Digest.Write`, by piece size:

| write size | xxhaste | zeebo/xxh3 | cespare (XXH64) |
|-----------:|--------:|-----------:|----------------:|
| 16 B | **2.63 GB/s** | 2.50 | 2.30 |
| 64 B | **6.61 GB/s** | 5.54 | 6.49 |
| 256 B | **9.49 GB/s** | 7.39 | 13.38 |
| 1 KiB | **14.51 GB/s** | 8.48 | 18.60 |
| 4 KiB | **16.99 GB/s** | 8.74 | 20.15 |
| one call | **19.6 GB/s** | 12.4 | 24.6 |

Ahead of the other XXH3 port at every size, and at 4 KiB pieces it retains 88%
of its own one-shot rate where cespare's XXH64 retains 76%. Tiny writes are
memmove-bound for everyone: at 16 bytes all three implementations sit within
13% of each other, because the cost is staging fragments, not hashing them.

The gain on long inputs comes from restructuring the accumulator rather than
from wider registers. XXH3 specifies

```
acc[i]   += mul32(data[i] ^ secret[i])
acc[i^1] += data[i]
```

The lane swap in the second line forces a shuffle per stripe. Keeping two
accumulators — one for the products, one for the raw data — and folding
`acc = accA + swap(accB)` only when the value is actually needed (once per
1 KiB block, not once per 64 bytes) removes it: five vector operations per
register per stripe instead of the reference's six, and two independent
dependency chains instead of one.

**amd64 numbers are not listed** because this was developed on arm64. The
kernels are correct there — see below — but unbenchmarked, so no claims are
made.

## Backends

| backend | selected when | verified by |
|---|---|---|
| AVX-512 | CPUID AVX512F + OS ZMM state | simulation |
| AVX2 | CPUID AVX2 + OS YMM state | executed under qemu |
| SSE2 | always available on amd64 | executed under qemu |
| SVE2 | HWCAP2_SVE2, vector length 256 or 512 | simulation |
| NEON | always available on arm64 | executed natively |
| SVE2 (VL 128) | generated; NEON is preferred at this width | executed natively |
| portable Go | other architectures, or `-tags purego` | executed natively |

`xxhaste.Backend()` reports which one is live.

SVE2 is generated once per vector length: a stripe is a fixed 64 bytes, and how
many registers that occupies is exactly what SVE leaves unspecified. At 128
bits it has no width advantage over NEON and needs an extra instruction per
stripe, so NEON wins and is chosen.

## Correctness

- **1512 reference vectors** generated from xxHash v0.8.3 C code cover every
  length class, four seeds, and eight custom secret sizes including ones whose
  length is not a multiple of the secret consume rate.
- **Every backend** is checked against those vectors, not just the one this
  machine dispatches to.
- **Streaming equals one-shot** across 26 chunking patterns × 25 lengths, plus
  randomized chunking.
- **Cross-implementation**: results are compared against zeebo/xxh3, an
  independent port, in `bench/`.

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
