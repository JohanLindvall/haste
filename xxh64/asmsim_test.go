package xxh64

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"

	"github.com/JohanLindvall/xxhaste/internal/asmgen"
)

// The generated kernels are checked here by executing them through the
// semantics recorded alongside each instruction when it was emitted, as the
// parent package does for its vector kernels. Every XXH64 backend goes
// through it, including the ones this machine cannot run natively; the ones
// it can are also executed in TestKernelsMatchPortable.

// simRegion lays out what a kernel sees: the lanes, the input and the primes
// table, each padded so an overrun lands in the padding and panics.
type simRegion struct {
	mem                    []byte
	m                      *asmgen.Machine
	lanesAt, inAt, tableAt uint64
	lanesOff               int
}

func newSimRegion(lanes *[4]uint64, in []byte) *simRegion {
	const pad = 64
	lanesOff := pad
	inOff := lanesOff + 32 + pad
	tableOff := inOff + len(in) + pad
	mem := make([]byte, tableOff+len(primes)*8+pad)
	for i, v := range lanes {
		binary.LittleEndian.PutUint64(mem[lanesOff+8*i:], v)
	}
	copy(mem[inOff:], in)
	for i, p := range primes {
		binary.LittleEndian.PutUint64(mem[tableOff+8*i:], p)
	}
	m := asmgen.NewMachine(mem, 1)
	return &simRegion{
		mem: mem, m: m, lanesOff: lanesOff,
		lanesAt: m.Base + uint64(lanesOff),
		inAt:    m.Base + uint64(inOff),
		tableAt: m.Base + uint64(tableOff),
	}
}

func (r *simRegion) lanes() [4]uint64 {
	var v [4]uint64
	for i := range v {
		v[i] = binary.LittleEndian.Uint64(r.mem[r.lanesOff+8*i:])
	}
	return v
}

// prologue reproduces what the hand-written Go assembly around the generated
// body does before it runs: the table's address into a register, or its
// constants into registers, depending on the backend. split is written into
// the table's sixth slot, where a dual backend's lane loop reads it; a
// single-form backend never looks.
func (r *simRegion) prologue(k asmgen.Kernel, def asmgen.FuncDef, split int) {
	if k.TableGPR() >= 0 {
		r.m.R[k.TableGPR()] = r.tableAt
	}
	r.m.Store64(r.tableAt+40, uint64(split))
	for _, l := range asmgen.PrologueLoads(k, def) {
		r.m.R[l.Reg] = r.m.Load64(r.tableAt + uint64(8*l.Slot))
	}
}

// simSum64 runs a backend's sum64 kernel over in.
func simSum64(t *testing.T, k asmgen.Kernel, def asmgen.FuncDef, in []byte, seed uint64, split int) uint64 {
	t.Helper()
	r := newSimRegion(&[4]uint64{}, in)
	r.prologue(k, def, split)
	r.m.R[k.ArgGPR(0)] = r.inAt
	r.m.R[k.ArgGPR(1)] = uint64(len(in))
	r.m.R[k.ArgGPR(2)] = seed
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	return r.m.R[k.RetGPR()]
}

func simBlocks(t *testing.T, k asmgen.Kernel, def asmgen.FuncDef, lanes *[4]uint64, in []byte, nb int, split int) {
	t.Helper()
	r := newSimRegion(lanes, in)
	r.prologue(k, def, split)
	r.m.R[k.ArgGPR(0)] = r.lanesAt
	r.m.R[k.ArgGPR(1)] = r.inAt
	r.m.R[k.ArgGPR(2)] = uint64(nb)
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	*lanes = r.lanes()
}

func TestSimulatedBackends(t *testing.T) {
	buf := testBuffer(3000)
	for _, b := range asmgen.XXH64Backends() {
		b := b
		forms := []int{0}
		if b.New64().Dual() {
			forms = []int{0, 1}
		}
		for _, split := range forms {
			split := split
			t.Run(fmt.Sprintf("%s-%s/split%d", b.Name, b.GOARCH, split), func(t *testing.T) {
				ks, defs := b.EmitAll(), b.Defs()
				sum64, blocks := ks[0], ks[1]
				sumDef, blockDef := defs[0], defs[1]
				// Every length that changes the tail's path, at every block
				// count the loop's odd/even split distinguishes, and a spread
				// beyond.
				var lens []int
				for n := 0; n < 4*blockLen+2*blockLen; n++ {
					lens = append(lens, n)
				}
				lens = append(lens, 255, 256, 257, 511, 512, 513, 1000, 1024, 2047, 2048, 3000)
				for _, n := range lens {
					for _, seed := range []uint64{0, 1, 0x9E3779B185EBCA87, ^uint64(0)} {
						if got, want := simSum64(t, sum64, sumDef, buf[:n], seed, split), sum64Generic(ptr(buf), n, seed); got != want {
							t.Fatalf("sum64 len %d seed %#x: %#016x != %#016x", n, seed, got, want)
						}
					}
				}
				// The reference vectors that fit the buffer, too.
				for _, v := range refVecs {
					if v.Len > 3000 {
						continue
					}
					if got := simSum64(t, sum64, sumDef, buf[:v.Len], v.Seed, split); got != v.H64 {
						t.Fatalf("vector len %d seed %#x: %#016x != %#016x", v.Len, v.Seed, got, v.H64)
					}
				}
				rng := rand.New(rand.NewSource(3))
				for i := 0; i < 200; i++ {
					var v, w [4]uint64
					for j := range v {
						v[j] = rng.Uint64()
					}
					w = v
					nb := rng.Intn(20)
					simBlocks(t, blocks, blockDef, &v, buf[:nb*blockLen], nb, split)
					blocksGeneric(&w, ptr(buf), nb)
					if v != w {
						t.Fatalf("blocks nb %d: %x != %x", nb, v, w)
					}
				}
			})
		}
	}
}
