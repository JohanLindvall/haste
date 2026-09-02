// Package asmgen generates the assembly kernels for haste.
//
// A backend is not written as assembly text. It is written as calls on a
// Builder, and each call records two things: the instruction in GNU assembler
// syntax, and a closure implementing that instruction's semantics. The text is
// handed to the system assembler, whose output becomes the hex in the .s files
// that ship; the closures are executed by Machine, which is how a backend can
// be checked against the reference on a machine that cannot run it.
//
// Because both come from the same call, a backend cannot be assembled from one
// description and verified against another.
package asmgen

import (
	"fmt"
	"strings"
)

// Inst is one emitted instruction, or a label if Label is set.
type Inst struct {
	Text  string
	Label string
	Sim   func(*Machine)
}

// Builder accumulates the instruction stream of one function.
type Builder struct {
	insts  []Inst
	nlabel int
}

func (b *Builder) emit(sim func(*Machine), format string, args ...any) {
	b.insts = append(b.insts, Inst{Text: fmt.Sprintf(format, args...), Sim: sim})
}

// Label marks the current position.
func (b *Builder) Label(name string) {
	b.insts = append(b.insts, Inst{Label: name, Sim: func(*Machine) {}})
}

// NewLabel returns a label name unique within this Builder.
func (b *Builder) NewLabel(prefix string) string {
	b.nlabel++
	return fmt.Sprintf(".L%s%d", prefix, b.nlabel)
}

// Insts returns the accumulated stream.
func (b *Builder) Insts() []Inst { return b.insts }

// Text renders the stream as an assembler source body.
func (b *Builder) Text() string {
	var sb strings.Builder
	for _, in := range b.insts {
		if in.Label != "" {
			fmt.Fprintf(&sb, "%s:\n", in.Label)
			continue
		}
		fmt.Fprintf(&sb, "\t%s\n", in.Text)
	}
	return sb.String()
}

// Standalone is the stripe index for a stripe emitted outside any unrolled
// group: the remainder loop, and the final stripe of a long input.
const Standalone = -1

// Cond is a branch condition. Comparisons are signed, matching the Go int
// arguments the kernels take.
type Cond int

const (
	LT Cond = iota
	GE
	EQ
	NE
	GT
	LE
)

func (c Cond) eval(a, bb uint64) bool {
	x, y := int64(a), int64(bb)
	switch c {
	case LT:
		return x < y
	case GE:
		return x >= y
	case EQ:
		return x == y
	case NE:
		return x != y
	case GT:
		return x > y
	case LE:
		return x <= y
	}
	panic("bad cond")
}

// GPR and VReg are architecture-independent register handles; each Arch maps
// them to its own names.
type GPR int

// VReg is a vector register. On arm64 the same number covers the NEON view
// (v0) and the SVE view (z0), which alias.
type VReg int

// FuncDef describes the Go function an emitted kernel implements. Every
// argument is pointer- or int-sized, and so is the result if there is one.
type FuncDef struct {
	Name string
	Args []string
	// Ret is the result type, or empty for none. A kernel that returns a
	// value leaves it in the register RetGPR names.
	Ret string
	// Table names a package-level variable the prologue reads constants
	// from, for a kernel that does not materialize them. Empty means none.
	// How it is reached is the architecture's: by address in TableGPR, or
	// slot by slot into registers -- see TableLoader.
	Table string
	Doc   string

	// FormJump names a kernel this one hands the whole call off to when
	// FormFlag is nonzero: the prologue tests the flag and, if set, jumps to
	// that symbol. Both must be ABI0 with the same signature, so the frame
	// and arguments are already in place and the callee's RET returns to
	// this one's caller.
	//
	// It exists so that a kernel with two forms can still be reached in one
	// direct call. Selecting in Go instead costs either an indirect call or
	// a second callee, and a second callee pushes the entry point past the
	// inliner's budget; see the XXH64 notes in CLAUDE.md. The cost here is a
	// compare against a byte in memory and a not-taken branch, which measured
	// nothing on a Redwood Cove.
	FormJump string
	FormFlag string
}

// TableLoad is one constant moved from the table into a register by the
// prologue, named by its slot index in 64-bit words.
type TableLoad struct {
	Slot int
	Reg  GPR
}

// TableLoader is implemented by a backend whose prologue lifts the table's
// constants straight into registers instead of keeping a pointer to it.
//
// It exists because on x86 the pointer is not free. A RIP-relative load has
// its address in the instruction, so it issues as soon as it is allocated; a
// load through a table pointer has to wait for the LEA that produced the
// pointer, and every multiply in the hash waits behind the prime it loads.
// Measured on Redwood Cove, moving the two lane-loop primes off the pointer
// was worth 5-15% of a 32..256-byte XXH64, and moving the rest off it the
// same again. See CLAUDE.md.
type TableLoader interface {
	// TableLoads is the slots this kernel's prologue loads, which may
	// differ per function: the streaming kernel needs only the two primes
	// its lane loop multiplies by.
	TableLoads(def FuncDef) []TableLoad
}

// PrologueLoads reports the constants k's prologue lands in registers before
// the body runs. A simulator has to set the same registers up, because the
// prologue is hand-written Go assembly and not part of the instruction
// stream.
func PrologueLoads(k Kernel, def FuncDef) []TableLoad {
	tl, ok := k.(TableLoader)
	if !ok || def.Table == "" {
		return nil
	}
	return tl.TableLoads(def)
}

// Kernel is what the renderer needs from an emitted function: which
// architecture it is for, its instruction stream, and how its arguments and
// result travel. Both the XXH3 vector backends and the XXH64 scalar ones
// implement it.
type Kernel interface {
	GOARCH() string
	Build() *Builder
	// ArgGPR is the register argument i is loaded into on entry.
	ArgGPR(i int) GPR
	// RetGPR is the register the result is stored from on exit, for kernels
	// with a result.
	RetGPR() GPR
	// TableGPR is the register the constant table's address is loaded into,
	// for kernels with a Table; -1 if this architecture does not use one.
	TableGPR() GPR
}

// Arch is the surface a backend needs: enough integer work to walk the input,
// plus the vector steps of the algorithm. Everything in kernel.go is written
// against this, so the loop structure exists once for all five backends.
type Arch interface {
	Kernel
	Name() string

	// TmpGPR is a scratch register (i in 0..5); ArgGPR, from Kernel, the
	// register holding argument i on entry.
	TmpGPR(i int) GPR
	GPRName(GPR) string

	MovRR(dst, src GPR)
	MovRI(dst GPR, imm int64)
	AddRR(dst, src GPR)
	AddRI(dst GPR, imm int64)
	SubRR(dst, src GPR)
	SubRI(dst GPR, imm int64)
	ShrRI(dst GPR, sh uint)
	ShlRI(dst GPR, sh uint)
	// The three-operand forms, for a result that lands in a register of
	// its own: dst = x + y, dst = x - y, dst = src - imm, dst = src >> sh,
	// dst = x + (y << sh), and dst = min(x, y) as signed. One instruction
	// each on arm64; on x86 a move and the two-operand form, which is what
	// the kernels there emitted anyway. They are worth six instructions of
	// a one-shot kernel's prologue and two per block on a core that
	// retires four a cycle.
	AddRRR(dst, x, y GPR)
	SubRRR(dst, x, y GPR)
	SubRRI(dst, src GPR, imm int64)
	ShrRRI(dst, src GPR, sh uint)
	AddShl(dst, x, y GPR, sh uint)
	Min(dst, x, y GPR)
	// BranchI compares a against an immediate, BranchR against a register.
	BranchI(a GPR, imm int64, c Cond, label string)
	BranchR(a, b GPR, c Cond, label string)
	Jmp(label string)
	// SubBranch subtracts imm from r and branches if the old value compared
	// with imm satisfies c -- equivalently, if the result does against zero.
	// It is the step and test of a counted loop in one flag-setting subtract
	// on both architectures.
	SubBranch(r GPR, imm int64, c Cond, label string)

	// Setup prepares whatever the vector steps need. scramble says whether
	// the kernel can reach a Scramble: a kernel that cannot does not need the
	// multiplier materialized, though it may still need predicates.
	Setup(scramble bool)
	Finish()

	// LoadAcc reads the eight accumulators from [p] into the product
	// accumulator and zeroes the data accumulator; StoreAcc writes them back.
	// StoreAcc must be preceded by Materialize. constant says that [p] is a
	// long-lived table rather than memory the caller has just written, which
	// on x86 decides the width of the load; see the x86 LoadAcc.
	LoadAcc(p GPR, constant bool)
	StoreAcc(p GPR)

	// GroupBegin runs once before an unrolled group's loop body, with the
	// secret pointer as it stands there. A backend may use it to hoist state
	// the body then maintains across iterations.
	GroupBegin(sec GPR)

	// Stripe absorbs one 64-byte stripe. k is the index within an unrolled
	// group, so a backend can rotate its scratch registers; Standalone means
	// the stripe is on its own, with no group state to rely on.
	Stripe(k int, in GPR, inOff int, sec GPR, secOff int)

	// FastBlockStripes is the block length, in stripes, that this backend can
	// run with the whole secret schedule held in registers, or 0 if it cannot.
	// A backend that returns n handles exactly the block a secretLimit of
	// n*secretConsumeRate produces, which is the default secret's; anything
	// else falls back to Stripe.
	FastBlockStripes() int

	// LoadSecretRegs fills those registers from the secret at [sec].
	LoadSecretRegs(sec GPR)

	// FastStripe absorbs stripe k of a block whose secret is in registers.
	FastStripe(k int, in GPR, inOff int)

	// Materialize folds the data accumulator into the product accumulator,
	// leaving the true XXH3 accumulator state in the latter. Unless final, it
	// also clears the data accumulator so absorption can continue; the last
	// one before a store does not, because nothing reads it again.
	Materialize(final bool)

	// Scramble applies the between-blocks scramble to the accumulators, which
	// must be materialized.
	Scramble(sec GPR, secOff int)

	// Unroll is how many stripes the inner loop does per iteration.
	Unroll() int

	// SecretImm reports whether the backend can address the secret at an
	// arbitrary immediate offset. SVE addresses in multiples of the vector
	// length, which the 8-byte-per-stripe secret schedule is not, so its
	// pointer is stepped instead.
	SecretImm() bool
}
