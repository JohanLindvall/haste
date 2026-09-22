# Streaming performance review, 2026-09-22

The improvements are in Go's streaming and finalization paths. Hash outputs,
public APIs, serialized state layouts, and generated kernels are unchanged.
The baseline is `4f8419e`.

Before integration, `main` independently acquired the same small-write and
buffered-finalization optimizations, plus constructor and XXH64 finalization
improvements. Those changes are retained. The measurements below document
this pass against its original baseline; they are not incremental gains over
`9288de1`. The additional runtime changes over that revision are seeded XXH3
digest reuse and restored-state validation, alongside the shared CPU probes
and removal of duplicate implementations.

## Measurement

Measured on an Intel Core Ultra 9 185H, pinned to Redwood Cove P-core 2,
Linux/amd64, Go 1.27.1. XXH3 selects AVX2, XXH64 the register-prime kernel,
and rapidhash the BMI2 kernel. These results do not establish performance on
other processors.

Each comparison used direct calls to the original and modified methods in
the same precompiled binary. Each cell had six 300 ms samples, with the
before/after order alternating between samples. Three binaries with different
live text padding covered both benchmark-caller address phases modulo 64;
`go tool nm` verified the addresses. No compilation or tests ran during these
measurements. CPU frequency was sampled every two seconds: median 4.50 GHz,
5th–95th percentile 4.30–4.70 GHz.

The tables report the range of reductions in median time across those three
layouts. Small changes should be read in that context: the untouched unseeded
64-byte XXH3 digest read varied from −0.1% to +0.5%.

## XXH3

| Operation | Input bytes | Reduction in time |
|---|---:|---:|
| Buffered `Digest.Sum64` | 256 | 28.5–30.4% |
| Buffered `Digest.Sum64` | 512 | 22.8–25.0% |
| Buffered `Digest.Sum64` | 1024 | 16.2–18.0% |
| Buffered `Digest.Sum128` | 256 | 23.9–24.7% |
| Seeded `Digest.Sum64` | 64 | 11.7–14.2% |
| Seeded `Digest.Sum128` | 64 | 3.6–8.2% |
| `Digest.Sum64` after 64-byte writes | 4096 | 1.9–2.7% |

These are finalization timings: construction and writing happen before the
timer. For scale, one layout measured the 256-byte `Sum64` at 15.175 →
10.845 ns and the 1024-byte case at 27.105 → 22.715 ns. They are not claims
about the cost of hashing an entire large stream.

Three changes account for the gains:

- In the measured candidate, when all input is still staged, `digestLong`
  calls `hashLong` once.
  That kernel already initializes the accumulators, walks block boundaries,
  and handles the final stripe. Previously finalization copied the initial
  accumulators and called two streaming kernels through another Go wrapper.
  Integration retains `main`'s equivalent routing through `sum64NS` and
  `sum128NS`, which enter the same whole-message kernel.
- For previously absorbed streams, finalization calls `accumBlocks` directly.
  The removed `consumeStripes` wrapper computed and returned a wrapped block
  position which its only caller discarded. The secret pointer is also
  selected once per finalization.
- Short seeded digest reads reuse `sum64Seeded` and `sum128Seeded`, including
  their inline 17–128-byte ladders. The old general seeded cores incurred an
  additional call at these sizes.

That last change made two old entry points and four out-of-line ladders
unused. Removing them eliminated 277 lines of duplicate implementations and
comments. The shared arithmetic primitives and portable kernel oracles remain.

## XXH64

| Write size, bytes | Reduction in time to stream 1 MiB |
|---:|---:|
| 1 | 15.9–16.6% |
| 8 | 17.1–19.3% |
| 16 | 14.6–16.0% |
| 31 | 3.8–4.4% |
| 32 | 4.0–4.7% |
| 64 | 2.4–3.1% |
| 256 | Within 0.1% |
| 1024 | Within 0.7% |

The initial profile of 16-byte writes attributed 34.8% of samples to `write`,
33.5% to the block kernel, and 14.1% to `runtime.memmove`. This made staging
overhead a better target than the multiply-bound kernel.

A paired hardware-counter probe supports the mechanism: instructions per
MiB at 16-byte writes fell from 8.42 million to 7.03 million, and cycles from
1.59 million to 1.37 million at 4.67 GHz. XXH3's 256-byte finalization fell
from about 325 to 229 instructions and from 26 to 18 taken branches.

The separate comparison suite confirmed the final writer change: 16-byte
writes measured 303.2 → 257.8 µs per MiB (15.0% less time), with 64-byte
writes 2.9% faster and 256/1024-byte writes within 0.7%. Cespare's control
rows moved at most 0.3%. These rows still reported zero allocations.

Writes that fit the staging buffer now use bounded first/last array moves.
Each move is at most 16 bytes; the compiler can emit larger array assignments
as calls to `memmove`. The copies use byte arrays to preserve support for
unaligned inputs, and never access bytes outside the supplied input or the
available buffer space. The buffer-completion and direct-block paths retain
their existing structure.

The writer also avoids its own stack check with `//go:nosplit`, as XXH3's
writer already does. Its callees retain their stack checks, and the linker
checks the resulting stack budget. Unsigned division of the nonnegative
remaining length removes signed-rounding instructions from the block path.
`Sum64` reads the lanes directly instead of copying them before a read-only
merge; the writer comparisons use the same finalizer on both sides.

Two broader rewrites were rejected. Advancing slices through the writer, and
combining staging and buffer completion, helped some fragmented workloads
but regressed aligned writes. Some cells lost 12–26% in those experiments.
The retained change preserves the direct-block path instead.

## Go idioms and duplication

- CPUID and XGETBV live in `internal/cpu`; all three amd64 dispatchers use the
  same probes. Backend selection policy remains with each hash. The test-only
  rapidhash override checks that leaf 7 exists before reading BMI2 support.
- XXH3's `Sum` uses `binary.BigEndian.AppendUint64`, matching XXH64.
- XXH64's marshaled-size constant includes its seed, and its documentation
  now describes the state it actually writes. The buffer-count check is the
  equivalent `n == total % blockLen`; the encoded bytes are unchanged.
- XXH3 now rejects a nonempty restored state with zero retained bytes. The
  previous validation accepted it, allowing `bufUsed-1` to underflow during
  finalization. A regression test reproduced the acceptance before the fix;
  state fuzzing now reads before writing, since a write could hide the defect.
- The fast staging copies stay local. Extracting a common small-copy helper
  cost 102 inliner nodes against the budget of 80 and would add a call to the
  path being optimized. Similarly, the active seeded and unseeded short hash
  cores retain their deliberate specializations.

Compiler diagnostics confirm that all 43 previously inlinable hash/write
entry points and thin wrappers still inline, on both Go 1.27.1 and Go 1.21.13.
No dependencies were added.

## Repeating the measurements

The retained `BenchmarkDigestSum` covers both result widths, default, seeded
and custom-secret modes, and the staging boundary after 64-byte writes.
`BenchmarkDigestChunkedLarge` in XXH64 includes odd write sizes to exercise
every buffer position. Build before timing:

```sh
go test -c -o /tmp/xxh3.test ./xxh3
taskset -c 2 /tmp/xxh3.test -test.run='^$' \
  -test.bench='^BenchmarkDigestSum$/^Default$/^(256|512|1024)$/^64$' \
  -test.benchtime=300ms -test.count=6 -test.benchmem

go test -c -o /tmp/xxh64.test ./xxh64
taskset -c 2 /tmp/xxh64.test -test.run='^$' \
  -test.bench='^BenchmarkDigestChunkedLarge$/^(1|8|16|31|32|64|256|1024)$' \
  -test.benchtime=300ms -test.count=6 -test.benchmem
```

For a comparison with `4f8419e`, use matching benchmark functions in a separate
baseline checkout. For changes below roughly 8%, follow the repository's
within-binary and relinking procedure rather than trusting one before/after
build. The temporary paired probes, logs, symbol addresses, clock samples,
and analysis scripts from this run are in `/tmp/haste-performance`.

## Correctness checks

The new tests cover reads followed by further writes across the XXH3 staging
boundary under default, seeded, and non-aligned custom secrets. XXH64 checks
every split through three blocks at sixteen input alignments, interleaving
slice writes, string writes, and empty writes.

Passed: native, purego, race, and race+purego suites; Go 1.21.13 native and
purego suites; both modules' `go vet`; comparison tests against the C and Go
reference implementations; and generated-file verification with no skipped
backends. All test packages cross-compile for thirteen Linux architectures
and five additional OS/architecture pairs, and the module builds for js/wasm.
Five focused fuzz targets passed for 60 seconds each: streaming equivalence
and state unmarshaling in both packages, plus XXH3 custom secrets.
Non-native targets were cross-compiled; the generator simulator also checked
the ARM64 and AVX-512 kernels. They were not timed or executed on hardware in
this pass.

After integration with `9288de1`, native, purego, both race configurations,
both Go 1.21.13 configurations, both modules' vet checks, reference comparisons,
the complete cross-build matrix, and generated-file verification passed again.
The restored-state fuzzer also passed a fresh 60-second run, executing about
5.8 million cases against the combined code.
