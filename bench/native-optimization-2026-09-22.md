# Digest performance analysis, 2026-09-22

This pass removes an allocation from local seeded and custom-secret XXH3
digests, reuses the one-shot implementation when a complete message is still
buffered, and reduces XXH64 finalization work. Hash values, public APIs,
serialized state, and generated assembly remain unchanged. The library still
has no dependencies outside the standard library.

Measurements are from Linux/arm64, Neoverse N2, two CPUs, Go 1.27.1. They
establish performance on this host, not on amd64 or other ARM cores.

## Retained changes

### XXH3 construction

`NewSeed` now allocates in a small, inlinable wrapper and calls a shared
initializer. Escape analysis can therefore keep a digest in the caller's
stack frame when its lifetime permits. `NewSecret` uses a composite literal
to initialize its accumulator and secret metadata, avoiding redundant reset
work and bringing that constructor within the inlining budget too. `New`
and `NewSeed` share default initialization; no pooling or new compiler
directives are needed.

The benchmark includes construction, writing the message, and `Sum64`.
Times are medians in ns/op; a negative change means less elapsed time.

| Message | Constructor | Before | After | Change |
|---|---|---:|---:|---:|
| 64 B | Default | 37.925 | 38.065 | +0.4% |
| 64 B | Seed | 297.250 | 46.380 | -84.4% |
| 64 B | Secret | 282.850 | 35.710 | -87.4% |
| 256 B | Default | 62.105 | 55.950 | -9.9% |
| 256 B | Seed | 323.650 | 63.895 | -80.3% |
| 256 B | Secret | 314.350 | 53.760 | -82.9% |
| 1 KiB | Default | 102.100 | 94.930 | -7.0% |
| 1 KiB | Seed | 342.750 | 103.300 | -69.9% |
| 1 KiB | Secret | 345.350 | 93.080 | -73.0% |
| 4 KiB | Default | 191.900 | 192.700 | +0.4% |
| 4 KiB | Seed | 466.000 | 200.650 | -56.9% |
| 4 KiB | Secret | 462.350 | 190.950 | -58.7% |

Every seeded/custom-secret row goes from **1536 B/op and one allocation to
zero**. Default construction already allocated nothing in this workload.
The seed varies each iteration; custom secrets are prepared before timing.
The `purego` measurements also confirm the allocation removal.

This benefit depends on the digest remaining local. `BenchmarkNewSeed`,
which stores the returned pointer globally, still allocates once and stays
approximately level. The optimization does not promise stack allocation
when a caller returns, stores, or otherwise lets the digest escape.

### XXH3 finalization

Messages through 1024 bytes remain contiguous in the staging buffer. Both
output widths now send these messages through the existing one-shot path,
which processes long inputs in one kernel call. The old finalizer entered
the streaming path above 240 bytes and used separate block and final-stripe
calls. Seeded inputs through 240 bytes still use the seed directly; longer
inputs use the secret already derived at construction.

For streams that have drained, finalization still copies the accumulators
and leaves the digest usable. It now calls `accumBlocks` directly, shares
the secret pointer with the final-stripe call, and removes `consumeStripes`,
whose returned block position was unused. Both widths share `digestLong`.

These measurements time finalization alone, after 64-byte writes. Thus the
1536-byte case includes an earlier drain followed by newly staged stripes.

| Default digest | Before ns | After ns | Change |
|---|---:|---:|---:|
| 256 B, Sum64 | 27.130 | 19.255 | -29.0% |
| 256 B, Sum128 | 32.430 | 24.120 | -25.6% |
| 1 KiB, Sum64 | 51.870 | 43.245 | -16.6% |
| 1 KiB, Sum128 | 56.865 | 48.365 | -14.9% |
| 1536 B, Sum64 | 35.755 | 31.250 | -12.6% |
| 1536 B, Sum128 | 40.795 | 38.080 | -6.7% |
| 4 KiB, Sum64 | 51.805 | 47.380 | -8.5% |
| 4 KiB, Sum128 | 56.805 | 54.225 | -4.5% |

Seeded and custom-secret digests show the same pattern. The full reset,
write, and Sum64 benchmark improves from 36.020 to 28.060 ns at 256 bytes
(-22.1%) and from 75.655 to 67.055 ns at 1 KiB (-11.4%).

There are small tradeoffs. Empty seeded finalization rises from 3.542 to
3.701 ns for Sum64 and from 4.143 to 4.326 ns for Sum128; empty
custom-secret Sum128 rises about 0.13 ns. Other short-input cells stay
within 3%. In `purego`, buffered finalization is roughly level: the 256-byte
Sum64 case is 2.4% slower, while Sum128 is 2.1% faster. Finalization after
an earlier drain improves by about 4% at 1536 bytes in both widths.

### XXH64 finalization

`Sum64` merges the lanes without copying their 32-byte array. When a long
message ends on a block boundary, it goes directly to the existing
avalanche function instead of decoding an empty tail. Short-message
finalization retains the portable implementation.

| Workload | Before ns | After ns | Change |
|---|---:|---:|---:|
| Sum64 after 32 B | 8.048 | 6.616 | -17.8% |
| Sum64 after 33 B | 8.515 | 8.111 | -4.8% |
| Reset + write + Sum64, 256 B | 23.975 | 22.030 | -8.1% |
| Reset + write + Sum64, 1 KiB | 52.495 | 50.070 | -4.6% |

Other measured block-aligned lengths show the same approximately 18%
finalization improvement. The `purego` build reproduces it, with no
short-message regression in the repeated measurements.

### Rapidhash and generator reuse

The ARM64 rapidhash generator now uses the common add, subtract, move,
and shift emitters instead of defining their instruction text and simulator
semantics again. Regeneration produces exactly the same assembly and Go
stubs. This is a maintenance improvement; no rapidhash runtime speedup is
claimed.

## Analysis across the library

The screening pass covered 691 native benchmark cells, including 350 API
boundary cells, strings, seeds, custom secrets, fixed-size entry points,
small writes, and every backend executable on this host. The only native
screening benchmark that allocates after the changes deliberately makes
`NewSeed` escape. Compiler diagnostics retain inlining for public one-shot,
fixed-size, and write wrappers, and confirm all three XXH3 constructors
inline. Allocation tests also pass on the minimum Go version, 1.21.13.

Backend selection remains appropriate for bulk hashing on this N2. At
64 KiB, the screening run measures XXH3's selected NEON hybrid at 2282 ns,
against 3137 ns for plain NEON, 2511 ns for the two-lane hybrid, and 3130 ns
for SVE2 VL128. XXH64's selected madd form measures 2443 ns against 3108 ns
for mul/add. These are screening figures, not new optimization claims.

The same-binary comparison suite gives this context for one-shot hashing.
Each cell is haste / the named implementation in ns/op, from six samples:

| Input | XXH3 / zeebo | XXH64 / cespare | Rapidhash / dw1 |
|---|---:|---:|---:|
| 64 B | 5.527 / 7.268 | 8.752 / 9.067 | 5.468 / 7.087 |
| 256 B | 17.900 / 24.975 | 16.830 / 18.710 | 13.500 / 18.380 |
| 1 KiB | 41.790 / 80.860 | 45.400 / 49.665 | 38.300 / 60.020 |
| 16 KiB | 570.550 / 1300.000 | 611.800 / 620.050 | 529.500 / 887.400 |

These compare implementations of each algorithm; they are not gains from
this patch. One-shot C timing was only screened because the cgo boundary
dominates small hashes. The reference C comparisons also ran as correctness
tests, without skips.

Bulk streaming controls remain within 1.4% for XXH3 and 0.5% for XXH64.
Some initial 1 MiB one-shot controls moved by 4–6%; paired rechecks reduce
the differences to -2.9% through +1.6%. Rapidhash's 16 KiB control remains
3.2% slower in these test binaries despite unchanged runtime source and
generated instructions. Changes to the linked test/generator code can
affect layout, so that difference is recorded rather than interpreted as
an algorithmic change. No bulk or one-shot improvement is attributed to
this pass.

Hardware counters corroborate the finalization improvements. Values below
include the benchmark loop and use `pstat`'s calibration-adjusted arithmetic:

| Workload | Cycles before / after | Instructions before / after |
|---|---:|---:|
| XXH3 Sum64, 256 B | 92.4 / 65.6 | 367.6 / 288.4 |
| XXH3 Sum64, 1536 B | 121.3 / 106.0 | 490.0 / 467.7 |
| XXH64 Sum64, 32 B | 27.3 / 22.4 | 115.0 / 93.2 |

The counters report 3.38–3.39 GHz and essentially no branch mispredictions
in these probes. The improvements remove work rather than depending on a
change in prediction. One-shot probes at 4, 64, 256, 1024, and 16384 bytes
for all three packages are preserved with the raw results.

## Experiments not retained

- Sending short seeded XXH3 digests through the larger seeded fast helpers
  regressed some cases by up to 13% on this N2.
- Sending short XXH64 digests through the assembly one-shot entry point
  helped native execution by about 5–10%, but regressed `purego` by
  7.5–16.5%.
- ARM64 rapidhash instruction reductions, including combined offset
  subtraction and paired loads, produced no broad gain. The shortest
  tested case improved about 3%, while other cases were neutral or slower.
- Reordering XXH3's length checks recovered the tiny empty seeded Sum128
  cost in a same-binary trial, but did not help empty seeded Sum64 and
  slightly worsened some longer cases. The simpler split was retained.

## Method and reproduction

The measured baseline is `43e8ef3971df49d775e195c42e8259f1882e99c5`.
Baseline and candidate binaries use identical benchmark sources; a Go
overlay substitutes the original implementation files for the baseline.
The changes were subsequently integrated onto `main` at `4a9a9e6`, whose
additional commits leave the measured ARM64 implementation unchanged.

All timed binaries were precompiled, pinned to CPU 1, and run with
`GOMAXPROCS=1`. No compilation or test suite ran concurrently with the
measurements. Native digest results use three paired rounds with alternating
revision order, two 300 ms samples per round. Purego, one-shot controls,
and comparisons use six 150 ms samples; the large-input rechecks use six
300 ms samples in alternating pairs. Screening cells use one 50 ms sample
and are not used to establish small percentage improvements. Earlier
same-binary implementations independently confirmed the finalizer gains.

The [median table](native-optimization-2026-09-22.csv) contains all 211
primary before/after cells, including allocations, sample counts, and
min/max timings. The [raw evidence](native-optimization-2026-09-22.tar.gz)
contains benchmark logs, rechecks, experiments, counters, host metadata,
and validation logs. It includes the rejected measurements too.

Representative commands, run from the module tree:

```sh
go test -c -o /tmp/xxh3.test ./xxh3
GOMAXPROCS=1 taskset -c 1 /tmp/xxh3.test -test.run='^$' \
  -test.bench='^Benchmark(NewSeed|Digest(Sum|Chunked|Local)?)$' \
  -test.benchtime=300ms -test.count=6 -test.benchmem
go test -tags=purego -c -o /tmp/xxh3-pure.test ./xxh3
GOMAXPROCS=1 taskset -c 1 /tmp/xxh3-pure.test -test.run='^$' \
  -test.bench='^BenchmarkDigest(Sum|Local)$' \
  -test.benchtime=150ms -test.count=6 -test.benchmem
# Repeat for xxh64; use BenchmarkSum and BenchmarkBackends for all packages.
```

## Validation

All passed, including a fresh integration check on the updated `main`:

- Native and `purego` tests, each with and without `-race`.
- Go 1.21.13 tests; root and benchmark-module vet checks.
- Benchmark-module comparisons against reference C, zeebo, cespare,
  bytedance, and dw1, without skipped reference comparisons.
- Native backend tests, portable comparisons, instruction simulation, and
  complete generated assembly/stub consistency with no skipped generators.
- Test-binary builds for Linux 386, arm, mips, mipsle, mips64, mips64le,
  s390x, ppc64, ppc64le, riscv64, loong64, arm64, and amd64; Windows amd64
  and arm64; Darwin amd64 and arm64; FreeBSD amd64; plus a js/wasm build.
- Four 60-second fuzz campaigns: XXH3 streaming, custom secrets, and
  unmarshalling; XXH64 streaming. The latest runs total over nine million
  executions.
- Expanded read-then-write tests verify repeated Sum64/Sum128 results and
  byte-identical serialized state across the buffering/drain boundaries,
  both seed extremes, and custom secrets of 136, 137, and 193 bytes.
- Formatting and `git diff --check`.

There was no native amd64 hardware or QEMU execution in this pass. Other
architectures are covered here by compilation and simulation, not by
performance measurements.
