package xxh3

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/JohanLindvall/haste/internal/asmgen"
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
// secret, then the initAcc table, each padded so an overrun lands in the
// padding and panics.
type simRegion struct {
	mem            []byte
	accAt, inAt    uint64
	secAt, initAt  uint64
	m              *asmgen.Machine
	accOff, secOff int
}

func newSimRegion(acc *[accNB]uint64, in, sec []byte) *simRegion {
	const pad = 64
	accOff := pad
	inOff := accOff + 8*accNB + pad
	secOff := inOff + len(in) + pad
	initOff := secOff + len(sec) + pad
	mem := make([]byte, initOff+8*accNB+pad)
	for i, v := range acc {
		binary.LittleEndian.PutUint64(mem[accOff+8*i:], v)
	}
	copy(mem[inOff:], in)
	copy(mem[secOff:], sec)
	for i, v := range initAcc {
		binary.LittleEndian.PutUint64(mem[initOff+8*i:], v)
	}

	m := asmgen.NewMachine(mem, accNB)
	return &simRegion{
		mem: mem, m: m, accOff: accOff, secOff: secOff,
		accAt:  m.Base + uint64(accOff),
		inAt:   m.Base + uint64(inOff),
		secAt:  m.Base + uint64(secOff),
		initAt: m.Base + uint64(initOff),
	}
}

func (r *simRegion) acc() [accNB]uint64 {
	var acc [accNB]uint64
	for i := range acc {
		acc[i] = binary.LittleEndian.Uint64(r.mem[r.accOff+8*i:])
	}
	return acc
}

// simHashLong runs a backend's hashLong kernel over the given input. The
// kernel starts from the initAcc table, whose address the prologue would put
// in TableGPR; acc is handed over as it is and must be ignored on entry.
func simHashLong(t *testing.T, k asmgen.Arch, acc *[accNB]uint64, in, sec []byte) {
	t.Helper()
	r := newSimRegion(acc, in, sec)
	r.m.R[k.TableGPR()] = r.initAt
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

// simAccumBlocks2 runs the two-run kernel: nbStripes stripes from the start
// of in, then nbStripes2 more from gap stripes further on, so that the second
// run is not the continuation of the first in memory.
func simAccumBlocks2(t *testing.T, k asmgen.Arch, acc *[accNB]uint64, in, sec []byte, nbStripes, soFar, gap, nbStripes2 int) {
	t.Helper()
	r := newSimRegion(acc, in, sec)
	r.m.R[k.ArgGPR(0)] = r.accAt
	r.m.R[k.ArgGPR(1)] = r.inAt
	r.m.R[k.ArgGPR(2)] = uint64(nbStripes)
	r.m.R[k.ArgGPR(3)] = r.secAt
	r.m.R[k.ArgGPR(4)] = uint64(len(sec) - stripeLen)
	r.m.R[k.ArgGPR(5)] = uint64(soFar)
	r.m.R[k.ArgGPR(6)] = r.inAt + uint64(stripeLen*(nbStripes+gap))
	r.m.R[k.ArgGPR(7)] = uint64(nbStripes2)
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
			hashLongK, blocksK, accumK, blocks2K := kernels[0], kernels[1], kernels[2], kernels[3]

			for _, n := range simLengths {
				in := buf[:n]
				want := initAcc
				hashLongGeneric(&want, unsafe.Pointer(&in[0]), n,
					unsafe.Pointer(&kSecret), secretDefaultSize-stripeLen)

				got := garbageAcc // output only, and must be ignored on entry
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

			// accumBlocks2 must carry the block position from the first run
			// into the second, wherever the first stops: short of the
			// boundary, on it, or past it, and either run may be empty.
			for _, soFar := range []int{0, 1, 15} {
				for _, n1 := range []int{0, 1, 15, 16, 17} {
					for _, n2 := range []int{0, 1, 15, 16, 31, 33} {
						const gap = 3
						in := buf[:stripeLen*(n1+gap+n2)]
						got, want := initAcc, initAcc
						accumBlocksGeneric(&want, unsafe.Pointer(&in[0]), n1,
							unsafe.Pointer(&kSecret), secretDefaultSize-stripeLen, soFar)
						accumBlocksGeneric(&want, add(unsafe.Pointer(&buf[0]), uintptr(stripeLen*(n1+gap))), n2,
							unsafe.Pointer(&kSecret), secretDefaultSize-stripeLen, (soFar+n1)%((secretDefaultSize-stripeLen)/secretConsumeRate))
						simAccumBlocks2(t, blocks2K, &got, in, sec, n1, soFar, gap, n2)
						if got != want {
							t.Fatalf("accumBlocks2 soFar=%d n1=%d n2=%d:\n got %v\nwant %v",
								soFar, n1, n2, got, want)
						}
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

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod. Backend.Filename is relative to the module root, and a test
// runs in its own package directory, so every path from the generator needs
// this in front of it.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// TestGeneratedFilesUpToDate regenerates every backend -- the XXH3 ones here
// and the XXH64 and rapidhash ones in their packages -- and compares it with
// what is checked in, so that an edit to the generator cannot silently leave
// the shipped assembly behind. The stub files, one per package and
// architecture, are checked the same way; they need no assembler, so they
// are checked even where the assembly is skipped.
func TestGeneratedFilesUpToDate(t *testing.T) {
	root := moduleRoot(t)
	if !testing.Short() {
		for _, b := range asmgen.AllBackends() {
			t.Run(b.Filename(), func(t *testing.T) {
				got, err := asmgen.Generate(b)
				if err != nil {
					t.Skipf("cannot assemble for %s: %v", b.GOARCH, err)
				}
				want, err := os.ReadFile(filepath.Join(root, b.Filename()))
				if err != nil {
					t.Fatalf("%v (run go generate ./...)", err)
				}
				if got != string(want) {
					t.Errorf("%s is stale; run go generate ./...", b.Filename())
				}
			})
		}
	}
	// The stubs, grouped as the generator groups them.
	type key struct{ dir, goarch string }
	byPkg := map[key][]asmgen.Backend{}
	var keys []key
	for _, b := range asmgen.AllBackends() {
		k := key{b.Dir, b.GOARCH}
		if _, seen := byPkg[k]; !seen {
			keys = append(keys, k)
		}
		byPkg[k] = append(byPkg[k], b)
	}
	for _, k := range keys {
		name := filepath.Join(k.dir, "stub_"+k.goarch+".go")
		t.Run(name, func(t *testing.T) {
			got, err := asmgen.GenerateStubs(k.goarch, byPkg[k])
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("%v (run go generate ./...)", err)
			}
			if got != string(want) {
				t.Errorf("%s is stale; run go generate ./...", name)
			}
		})
	}
}
