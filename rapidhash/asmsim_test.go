package rapidhash

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/JohanLindvall/haste/internal/asmgen"
)

// The generated kernels are checked here by executing them through the
// semantics recorded alongside each instruction when it was emitted, the way
// both sibling packages check theirs. It is what verifies a backend this
// machine cannot run natively -- and, for the one it can, it is the check
// that runs before the assembler is ever involved.

// simRegion lays out what a kernel sees: the input and the secret table, each
// padded so an overrun lands in the padding and panics rather than reading
// something plausible.
type simRegion struct {
	mem           []byte
	m             *asmgen.Machine
	inAt, tableAt uint64
}

func newSimRegion(in []byte) *simRegion {
	const pad = 64
	inOff := pad
	tableOff := inOff + len(in) + pad
	mem := make([]byte, tableOff+len(secret)*8+pad)
	copy(mem[inOff:], in)
	for i, w := range secret {
		binary.LittleEndian.PutUint64(mem[tableOff+8*i:], w)
	}
	m := asmgen.NewMachine(mem, 1)
	return &simRegion{
		mem: mem, m: m,
		inAt:    m.Base + uint64(inOff),
		tableAt: m.Base + uint64(tableOff),
	}
}

// simSum64 runs a backend's kernel over in, reproducing what the hand-written
// Go assembly around the generated body does first: the table's address into
// its register, then the arguments.
func simSum64(t *testing.T, k asmgen.Kernel, def asmgen.FuncDef, in []byte, seed uint64) uint64 {
	t.Helper()
	r := newSimRegion(in)
	if k.TableGPR() >= 0 {
		r.m.R[k.TableGPR()] = r.tableAt
	}
	for _, l := range asmgen.PrologueLoads(k, def) {
		r.m.R[l.Reg] = r.m.Load64(r.tableAt + uint64(8*l.Slot))
	}
	r.m.R[k.ArgGPR(0)] = r.inAt
	r.m.R[k.ArgGPR(1)] = uint64(len(in))
	r.m.R[k.ArgGPR(2)] = seed
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	return r.m.R[k.RetGPR()]
}

// simSum64NS is simSum64 for the unseeded twin, which takes no seed.
func simSum64NS(t *testing.T, k asmgen.Kernel, def asmgen.FuncDef, in []byte) uint64 {
	t.Helper()
	r := newSimRegion(in)
	if k.TableGPR() >= 0 {
		r.m.R[k.TableGPR()] = r.tableAt
	}
	for _, l := range asmgen.PrologueLoads(k, def) {
		r.m.R[l.Reg] = r.m.Load64(r.tableAt + uint64(8*l.Slot))
	}
	r.m.R[k.ArgGPR(0)] = r.inAt
	r.m.R[k.ArgGPR(1)] = uint64(len(in))
	if err := r.m.Run(k.Build().Insts()); err != nil {
		t.Fatal(err)
	}
	return r.m.R[k.RetGPR()]
}

// TestSimulatedBackends is the kernels against the portable implementation at
// every length that changes a path, and against the C-derived vectors on top
// of that. Every length to 512 is covered one at a time: the three length
// classes, both block loops, all six rungs of the ladder, and the boundaries
// between them are all in that range, and the range is cheap enough to walk
// exhaustively rather than to sample.
func TestSimulatedBackends(t *testing.T) {
	buf := testBuffer(2048)
	seeds := []uint64{0, 1, 0x9e3779b185ebca87, ^uint64(0)}

	for _, b := range asmgen.RapidBackends() {
		b := b
		t.Run(fmt.Sprintf("%s-%s", b.Name, b.GOARCH), func(t *testing.T) {
			ks, defs := b.EmitAll(), b.Defs()
			k, def := ks[0], defs[0]

			var lens []int
			for n := 0; n <= 512; n++ {
				lens = append(lens, n)
			}
			lens = append(lens, 671, 672, 673, 895, 896, 897, 1024, 1337, 2048)

			for _, n := range lens {
				for _, seed := range seeds {
					want := sum64Generic(ptr(buf), n, seed)
					if got := simSum64(t, k, def, buf[:n], seed); got != want {
						t.Fatalf("len=%d seed=%#x: kernel %#016x != portable %#016x",
							n, seed, got, want)
					}
				}
			}

			// The unseeded twin is a second instruction stream with its
			// own prologue, so nothing above touches it: run it over the
			// same lengths against the portable path with a zero seed, and
			// over the unseeded vectors.
			nsK, nsDef := ks[1], defs[1]
			for _, n := range lens {
				want := sum64Generic(ptr(buf), n, 0)
				if got := simSum64NS(t, nsK, nsDef, buf[:n]); got != want {
					t.Fatalf("NS len=%d: kernel %#016x != portable %#016x", n, got, want)
				}
			}
			for _, v := range refVecs {
				if v.Len > 2048 || v.Seed != 0 {
					continue
				}
				if got := simSum64NS(t, nsK, nsDef, buf[:v.Len]); got != v.H64 {
					t.Fatalf("NS vector len=%d: %#016x != %#016x", v.Len, got, v.H64)
				}
			}

			// And the vectors, which come from the C implementation rather
			// than from this package's own idea of the algorithm.
			checked := 0
			for _, v := range refVecs {
				if v.Len > 2048 {
					continue
				}
				if got := simSum64(t, k, def, buf[:v.Len], v.Seed); got != v.H64 {
					t.Fatalf("vector len=%d seed=%#x: %#016x != %#016x",
						v.Len, v.Seed, got, v.H64)
				}
				checked++
			}
			t.Logf("%d lengths and %d reference vectors reproduced", len(lens)*len(seeds), checked)
		})
	}
}
