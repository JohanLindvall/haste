# Native optimization pass, 2026-09-09

Measured on an AMD Ryzen 7 8840HS (Zen 4), Linux/amd64, Go 1.27.1,
pinned to CPU 12. The baseline is commit `4f8419e`. No additional native
host was available: ARM64 results below are correctness checks, not timings.

## Measured x86 candidate

The XXH64 candidate stages both small writes and the remainder of a large
write through the same fixed-size copies. At most 31 bytes remain, so
16-, 8-, 4-, and 1-byte moves avoid a call to `runtime.memmove` without
reading or writing outside the input or staging area. Whole blocks still
use the selected assembly kernel. Nonnegative block counts use unsigned
division, and an exactly consumed input returns before forming a pointer
past its end. Hash outputs, APIs, and serialized state layout are unchanged.

The old and new write methods were linked into one test binary, using the
same Reset, Sum64, input, and selected backend. Each row hashes 1 MiB in
chunks; values are median **time reductions**, not throughput increases.
Final samples used 150 ms per cell, four repetitions, with longer preliminary
passes. Separate old/new binaries gave misleadingly large changes and were
not used for these figures.

| Write size | scalar | scalar-ptr | purego |
|---|---:|---:|---:|
| 7 bytes | 21.5% | 21.6% | 23.1% |
| 16 bytes | 15.8% | 13.2% | 12.1% |
| 64 bytes | 4.2% | 3.7% | 8.4% |
| 65 bytes | 5.3% | 5.9% | 10.7% |
| 256 bytes | 6.0% | 5.8% | 4.6% |
| 64 KiB | 0.1% | 1.5% | 0.0% |

The 31- and 33-byte cases on assembly backends were approximately unchanged.
Treat differences below about 2% as noise. GOAMD64=v3 and v4 also retain the
small-write gains; no build level or CPU dispatch change is required.

## Other native paths

- XXH3: measured SSE2, AVX2, and AVX-512. Fixed copies in the small-write
  drain saved about 1–3% on SSE2/AVX2, but repeatedly cost 5–6% for AVX-512
  with 256-byte writes. Always copying both overlapping stripes still cost
  4.4% there. Both variants were reverted.
- XXH64: inlining the completed partial block saved time for 16-byte writes,
  but cost 6–8% at 31- and 65-byte writes. Reverted; the retained change
  preserves the assembly call for a completed partial block.
- rapidhash: measured scalar and BMI2/mulx in one binary. The paired mulx
  loop took 11% more time at 1 KiB and about 6% more at 64 KiB and 1 MiB.
  The existing selection of scalar on this CPU remains appropriate.

## Repeating the benchmarks

Both xxHash packages now expose `BenchmarkDigestBackends`, so every runnable
backend is measured for streaming as well as one-shot hashing. XXH3 includes
63-, 65-, and 960-byte writes; XXH64 includes partial-block and aligned writes.
Compile before timing and run each binary separately:

```sh
go test -c -o /tmp/xxh64.test ./xxh64
taskset -c 12 /tmp/xxh64.test -test.run='^$' \
  -test.bench='BenchmarkDigestBackends' -test.benchtime=300ms -test.count=6
go test -c -o /tmp/xxh3.test ./xxh3
taskset -c 12 /tmp/xxh3.test -test.run='^$' \
  -test.bench='BenchmarkBackends|BenchmarkDigestBackends' \
  -test.benchtime=300ms -test.count=6
```

Run the same commands with `-tags purego` on the compilation step for the
portable implementation. Compare matching hardware and compiler settings,
and keep unchanged large-write cases as controls for timing drift.
Use the same benchmark source for both revisions: adding unaligned chunks
also added a final-chunk bounds check to the XXH3 benchmark loop.

## Validation

All checks passed:

- Native and purego suites, both with and without the race detector; vet
  and the separate bench module's C/reference comparisons.
- GOAMD64=v2, v3, and v4 suites; native execution of the 386 test binaries.
- Strict pointer checking (`-gcflags=all=-d=checkptr=2`) for XXH64.
- Every generated assembly/stub check, with no skips, and the instruction
  simulators for all backends.
- Sixty seconds of XXH64 streaming fuzzing per configuration: 1,859,738
  native executions and 2,621,816 purego executions.
- Linux cross-compilation for 386, arm, mips, mipsle, mips64, s390x, ppc64,
  ppc64le, riscv64, loong64, and arm64; Windows and Darwin on amd64/arm64,
  FreeBSD/amd64, and a js/wasm build.
- All three packages under ARM64 QEMU (`-cpu max`, `-test.short`).
- Native and purego execution of every new streaming benchmark with a
  single iteration, as a coverage check rather than a timing measurement.

The added partial-block test checks every staging offset, all write lengths
through 65 bytes, two seeds, both Write and WriteString, and continuation
after reading the digest, on each available XXH64 backend.

## Integration with main

Main independently received the [N2 optimization pass](native-optimization-2026-09-09.md)
while this x86 pass was in progress. Integration preserves its XXH3 seed
derivation changes and its complete XXH64 implementation, including `nosplit`.
Both passes independently found the small-write copy improvement. A final
comparison with main found mixed results from additionally sharing the
remainder-copy path: about 3.2% less time with 65-byte writes but 3.3% more
with 31-byte writes, with large writes level. That extra restructuring was
therefore dropped during integration. Main's 64 KiB `BenchmarkDigestChunked` remains intact;
this pass's 1 MiB harness is named `BenchmarkDigestChunkedLarge` and is used
by `BenchmarkDigestBackends`. The timing table above records the original
comparison with `4f8419e`, not an additional improvement over the N2 pass.
Native/purego tests, both race configurations, vet, and the bench-module
reference comparisons were rerun successfully after integration.
