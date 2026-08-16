package xxhaste

import (
	"encoding/binary"
	"os"
	"testing"
	"unsafe"

	"github.com/JohanLindvall/xxhaste/internal/asmgen"
)

// The generated kernels are checked here by executing them, instruction by
// instruction, through the semantics recorded alongside each one when it was
// emitted. That is the only way some of them can be checked at all on this
// machine: there is no AVX-512 emulator to hand, and an SVE vector length
// cannot be raised above what the hardware has.
//
// What this proves is that the instruction sequence computes XXH3. What it
// cannot prove is that the assembler encoded those instructions correctly --
// but the bytes come from the system assembler rather than from this package,
// and the .s files carry the disassembly of exactly those bytes. The backends
// that can run natively are additionally executed in TestBackendsNative and
// under qemu.

// simRegion lays out the memory a kernel sees: accumulators, then input, then
// secret, each padded so an overrun lands in the padding and panics.
type simRegion struct {
	mem            []byte
	accAt, inAt    uint64
	secAt          uint64
	m              *asmgen.Machine
	accOff, secOff int
}

func newSimRegion(acc *[accNB]uint64, in, sec []byte) *simRegion {
	const pad = 64
	accOff := pad
	inOff := accOff + 8*accNB + pad
	secOff := inOff + len(in) + pad
	mem := make([]byte, secOff+len(sec)+pad)
	for i, v := range acc {
		binary.LittleEndian.PutUint64(mem[accOff+8*i:], v)
	}
	copy(mem[inOff:], in)
	copy(mem[secOff:], sec)

	m := asmgen.NewMachine(mem, accNB)
	return &simRegion{
		mem: mem, m: m, accOff: accOff, secOff: secOff,
		accAt: m.Base + uint64(accOff),
		inAt:  m.Base + uint64(inOff),
		secAt: m.Base + uint64(secOff),
	}
}

func (r *simRegion) acc() [accNB]uint64 {
	var acc [accNB]uint64
	for i := range acc {
		acc[i] = binary.LittleEndian.Uint64(r.mem[r.accOff+8*i:])
	}
	return acc
}

// simHashLong runs a backend's hashLong kernel over the given input.
func simHashLong(t *testing.T, k asmgen.Arch, acc *[accNB]uint64, in, sec []byte) {
	t.Helper()
	r := newSimRegion(acc, in, sec)
	r.m.R[k.ArgGPR(0)] = r.accAt
	r.m.R[k.ArgGPR(1)] = r.inAt
	r.m.R[k.ArgGPR(2)] = uint64(len(in))
	r.m.R[k.ArgGPR(3)] = r.secAt
	r.m.R[k.ArgGPR(4)] = uint64(len(sec) - stripeLen)
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	*acc = r.acc()
}

func simAccum(t *testing.T, k asmgen.Arch, acc *[accNB]uint64, in, sec []byte, nbStripes int) {
	t.Helper()
	r := newSimRegion(acc, in, sec)
	r.m.R[k.ArgGPR(0)] = r.accAt
	r.m.R[k.ArgGPR(1)] = r.inAt
	r.m.R[k.ArgGPR(2)] = uint64(nbStripes)
	r.m.R[k.ArgGPR(3)] = r.secAt
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	*acc = r.acc()
}

func simAccumBlocks(t *testing.T, k asmgen.Arch, acc *[accNB]uint64, in, sec []byte, nbStripes, soFar int) {
	t.Helper()
	r := newSimRegion(acc, in, sec)
	r.m.R[k.ArgGPR(0)] = r.accAt
	r.m.R[k.ArgGPR(1)] = r.inAt
	r.m.R[k.ArgGPR(2)] = uint64(nbStripes)
	r.m.R[k.ArgGPR(3)] = r.secAt
	r.m.R[k.ArgGPR(4)] = uint64(len(sec) - stripeLen)
	r.m.R[k.ArgGPR(5)] = uint64(soFar)
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	*acc = r.acc()
}

// simLengths spans every structural boundary of the long path: the first
// length that reaches it, exact multiples of the stripe and block, and inputs
// long enough to need several blocks and an unrolled remainder.
var simLengths = []int{
	241, 255, 256, 257, 319, 320, 321, 383, 384, 512, 575, 576, 577,
	1023, 1024, 1025, 1087, 1088, 1089, 2047, 2048, 2049, 3072,
	4096, 5000, 8192, 9000, 16384, 20000,
}

func TestSimulatedBackends(t *testing.T) {
	buf := testBuffer(20000)
	sec := kSecret[:]
	for _, b := range asmgen.Backends() {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			kernels := asmgen.EmitAll(b.New)
			hashLongK, blocksK, accumK := kernels[0], kernels[1], kernels[2]

			for _, n := range simLengths {
				in := buf[:n]
				want := initAcc
				hashLongGeneric(&want, unsafe.Pointer(&in[0]), n,
					unsafe.Pointer(&kSecret), secretDefaultSize-stripeLen)

				got := initAcc
				simHashLong(t, hashLongK, &got, in, sec)
				if got != want {
					t.Fatalf("hashLong len=%d:\n got %v\nwant %v", n, got, want)
				}

				// The same input must also come out right through the
				// streaming entry points, which the Digest drives.
				if n >= 4*stripeLen {
					stripes := 4
					gotA, wantA := initAcc, initAcc
					accumulateGeneric(&wantA, unsafe.Pointer(&in[0]), unsafe.Pointer(&kSecret), stripes)
					simAccum(t, accumK, &gotA, in, sec, stripes)
					if gotA != wantA {
						t.Fatalf("accum len=%d:\n got %v\nwant %v", n, gotA, wantA)
					}
				}
			}

			// Odd stripe counts exercise the remainder loop after the
			// unrolled one.
			for _, stripes := range []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 16, 17} {
				in := buf[:stripeLen*stripes+stripeLen]
				got, want := initAcc, initAcc
				accumulateGeneric(&want, unsafe.Pointer(&in[0]), unsafe.Pointer(&kSecret), stripes)
				simAccum(t, accumK, &got, in, sec, stripes)
				if got != want {
					t.Fatalf("accum stripes=%d:\n got %v\nwant %v", stripes, got, want)
				}
			}

			// accumBlocks has to scramble at exactly the boundaries the
			// portable walk does, from any starting position in the block.
			for _, soFar := range []int{0, 1, 7, 15} {
				for _, stripes := range []int{1, 2, 9, 15, 16, 17, 31, 32, 33, 48} {
					in := buf[:stripeLen*stripes]
					got, want := initAcc, initAcc
					accumBlocksGeneric(&want, unsafe.Pointer(&in[0]), stripes,
						unsafe.Pointer(&kSecret), secretDefaultSize-stripeLen, soFar)
					simAccumBlocks(t, blocksK, &got, in, sec, stripes, soFar)
					if got != want {
						t.Fatalf("accumBlocks soFar=%d stripes=%d:\n got %v\nwant %v",
							soFar, stripes, got, want)
					}
				}
			}
		})
	}
}

// TestSimulatedCustomSecret covers the secret sizes that change the block
// length, including one whose limit is not a multiple of the consume rate.
func TestSimulatedCustomSecret(t *testing.T) {
	buf := testBuffer(9000)
	for _, b := range asmgen.Backends() {
		t.Run(b.Name, func(t *testing.T) {
			k := asmgen.EmitAll(b.New)[0]
			for _, secLen := range []int{136, 137, 144, 191, 192, 193, 256, 1024} {
				sec := testSecret(secLen)
				for _, n := range []int{241, 600, 1025, 4096, 9000} {
					in := buf[:n]
					want := initAcc
					hashLongGeneric(&want, unsafe.Pointer(&in[0]), n,
						unsafe.Pointer(&sec[0]), secLen-stripeLen)
					got := initAcc
					simHashLong(t, k, &got, in, sec)
					if got != want {
						t.Fatalf("hashLong len=%d secretLen=%d:\n got %v\nwant %v",
							n, secLen, got, want)
					}
				}
			}
		})
	}
}

// TestSimulatedAgainstReference closes the loop: the simulated accumulators go
// through the same convergence the library uses, and the result is compared
// with the vectors taken from the C implementation.
func TestSimulatedAgainstReference(t *testing.T) {
	buf := testBuffer(maxVecLen())
	for _, b := range asmgen.Backends() {
		t.Run(b.Name, func(t *testing.T) {
			k := asmgen.EmitAll(b.New)[0]
			checked := 0
			for _, v := range refVecs {
				if v.Len <= midsizeMax || v.Len > 100000 || v.Seed != 0 {
					continue
				}
				in := buf[:v.Len]
				acc := initAcc
				simHashLong(t, k, &acc, in, kSecret[:])
				h64 := mergeAccs(&acc, unsafe.Pointer(&kSecret[secretMergeAccsStart]),
					uint64(v.Len)*prime64_1)
				if h64 != v.H64 {
					t.Fatalf("len=%d: %#016x, want %#016x", v.Len, h64, v.H64)
				}
				lo := h64
				hi := mergeAccs(&acc, unsafe.Pointer(&kSecret[secretDefaultSize-8*accNB-secretMergeAccsStart]),
					^(uint64(v.Len) * prime64_2))
				if lo != v.Lo || hi != v.Hi {
					t.Fatalf("len=%d: 128-bit {%#016x,%#016x}, want {%#016x,%#016x}",
						v.Len, lo, hi, v.Lo, v.Hi)
				}
				checked++
			}
			if checked == 0 {
				t.Fatal("no vectors exercised the long path")
			}
			t.Logf("%d reference vectors reproduced", checked)
		})
	}
}

// TestGeneratedFilesUpToDate regenerates every backend -- the XXH3 ones here
// and the XXH64 ones in xxh64/ -- and compares it with what is checked in, so
// that an edit to the generator cannot silently leave the shipped assembly
// behind.
func TestGeneratedFilesUpToDate(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the system assembler")
	}
	for _, b := range asmgen.AllBackends() {
		t.Run(b.Filename(), func(t *testing.T) {
			got, err := asmgen.Generate(b)
			if err != nil {
				t.Skipf("cannot assemble for %s: %v", b.GOARCH, err)
			}
			want, err := os.ReadFile(b.Filename())
			if err != nil {
				t.Fatalf("%v (run go generate ./...)", err)
			}
			if got != string(want) {
				t.Errorf("%s is stale; run go generate ./...", b.Filename())
			}
		})
	}
}
