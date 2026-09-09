# Native optimization pass, 2026-09-09

Host: Linux/arm64, Neoverse N2, two CPUs, Go 1.27.1. Benchmarks use
precompiled test binaries pinned to CPU 1. The baseline is the original
library at `4f8419e` with the expanded benchmark harness; the candidate uses the same
harness. No compilation or test suite runs concurrently with the final
benchmark pass. Changes retain the hash algorithms and serialized formats.

## Changes

- XXH64 stages writes of fewer than 32 bytes with bounded, overlapping
  head/tail copies, avoiding a call to `runtime.memmove`. A copy runs only
  when the entire write fits the private staging buffer. Empty writes read
  nothing. The block count uses unsigned division because the remainder is
  nonnegative. `Write` and `WriteString` share this implementation.
- XXH3 derives seeded secrets in three 64-byte windows instead of twelve
  16-byte iterations. Constant offsets remove repeated indexing and bounds
  checks while retaining endian conversion. This serves both hash widths,
  seeded strings, and `NewSeed`.
- Added direct-call benchmarks for string, seeded-string, zero-seed,
  custom-secret, fixed-size XXH64, and small streaming writes. The API sweep
  includes empty input and length boundaries from 1 through 241 bytes,
  plus 256 B, 1 KiB, and 16 KiB. Rapidhash fixed-size benchmarks now vary
  the input each iteration.
- Added a small-write regression test covering every staged position,
  lengths 0..64, input offsets 0..7, both seeds, byte/string writes, and
  continued hashing, on each available XXH64 backend.

## Measurement

The API boundary sweep is a screening run (30 ms per cell). Performance
claims use medians of six 150 ms samples; the targeted rapidhash experiment
used six 300 ms samples. Every backend executable with this host's vector
length is included by `BenchmarkBackends`: XXH3 NEON, four-lane hybrid,
two-lane hybrid, SVE2 VL128; XXH64 madd/muladd; rapidhash scalar. Wider SVE2
and x86 kernels cannot be timed natively on this host.

Reproduce from each checkout with the same benchmark files:

```sh
go test -c -o /tmp/xxh64.test ./xxh64
taskset -c 1 /tmp/xxh64.test -test.run='^$' \
  -test.bench='Benchmark(Sum|Fixed|Digest|Backends)' \
  -test.benchtime=150ms -test.count=6
# Repeat for xxh3 and rapidhash; include SeededLong and NewSeed for xxh3.
```

## Rejected experiments

A separate unseeded ARM64 XXH64 kernel was mostly level with the existing
kernel (generally within 2%); one short-length gain did not justify another
copy of both block loops. Rapidhash fixed-size secret constants were 5–21%
slower than table loads on this host and were reverted. Splitting XXH64's
block processing into a helper sped up tiny writes but regressed whole-block
writes; the retained implementation keeps both paths in the same function.

## Retained improvements

Times are ns/op; XXH64 streaming rows hash a 64 KiB message. Percentages
are changes in elapsed time, so negative is faster.

| Workload | Before | After | Change |
|---|---:|---:|---:|

| XXH64, 1 B writes | 314088.000 | 240020.000 | -23.6% |
| XXH64, 4 B writes | 79113.000 | 60165.500 | -23.9% |
| XXH64, 8 B writes | 42879.500 | 34755.500 | -18.9% |
| XXH64, 16 B writes | 25161.000 | 22379.000 | -11.1% |
| XXH64, 32 B writes | 14315.500 | 13624.000 | -4.8% |
| XXH64, 64 B writes | 8269.000 | 8236.500 | -0.4% |
| XXH64, 256 B writes | 4004.000 | 4021.000 | +0.4% |
| XXH64, 1024 B writes | 2875.000 | 2838.000 | -1.3% |
| XXH64, 4096 B writes | 2537.500 | 2547.000 | +0.4% |
| XXH3 seeded 64-bit, 241 B | 32.650 | 28.575 | -12.5% |
| XXH3 seeded 64-bit, 256 B | 32.525 | 28.490 | -12.4% |
| XXH3 seeded 64-bit, 512 B | 40.830 | 36.680 | -10.2% |
| XXH3 seeded 64-bit, 1024 B | 56.940 | 52.810 | -7.3% |
| XXH3 seeded 128-bit, 241 B | 37.375 | 33.545 | -10.2% |
| XXH3 seeded 128-bit, 256 B | 37.275 | 33.465 | -10.2% |
| XXH3 seeded 128-bit, 512 B | 45.605 | 41.655 | -8.7% |
| XXH3 seeded 128-bit, 1024 B | 61.580 | 57.735 | -6.2% |

The untargeted 1 MiB controls vary by a few percent between runs (XXH64
-3.5%, XXH3 +3.6% in this pair), despite their hash loops being unchanged.
These are not counted as improvements; the targeted changes operate on
small streaming writes and per-seed setup. There are no runtime dispatch
changes or new architecture-specific kernels in the final patch.

The [complete median table](native-optimization-2026-09-09.csv) contains all
224 repeated benchmark cells. The separate API boundary sweep covers 350
cells per revision. Untargeted repeated cells below 1 MiB stay within 3%
of baseline, including every executable backend; rapidhash production code
is unchanged. These measurements establish results on this N2 only.

## Validation

All passed:

- Native and `purego` tests, each with and without `-race`.
- Go 1.21.13 tests, plus root and benchmark-module `go vet`.
- Benchmark-module tests and explicit C, zeebo, and cespare cross-checks;
  the reference checks ran without skips.
- Linux test-binary builds for 386, arm, mips, mipsle, mips64, s390x,
  ppc64, ppc64le, riscv64, loong64, arm64, and amd64; js/wasm build.
- Generated assembly/stub consistency, with no skipped backends.
- Four 20-second fuzz runs: XXH64 streaming and kernel agreement;
  XXH3 streaming and custom secrets.
- All XXH64 one-shot wrappers and six fixed-size entry points still inline.
- Formatting and `git diff --check`.

`AGENTS.md` is a relative symlink to `CLAUDE.md`, as requested.
