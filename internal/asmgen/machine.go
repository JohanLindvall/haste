package asmgen

import "fmt"

// Machine executes an instruction stream through the closures recorded by the
// Builder. It exists so that a backend can be checked against the reference
// output on hardware that cannot run it: AVX-512 has no emulator here, and SVE2
// vector lengths other than this machine's cannot be requested at runtime.
//
// It is not a general CPU model. It implements exactly the instructions the
// backends emit, over a flat address space that starts at Base.
type Machine struct {
	R [32]uint64
	// V holds vector registers as eight 64-bit lanes, enough for a 512-bit
	// register. Narrower backends use the low lanes.
	V [32][8]uint64

	Mem  []byte
	Base uint64

	PC    int
	Steps int

	// cmpA and cmpB hold the operands of the last comparison; branches read
	// them rather than a flags word, which keeps the two halves independent
	// of any particular architecture's flag semantics.
	cmpA, cmpB uint64

	// Lanes is the vector width in 64-bit lanes for the backend being run.
	Lanes int

	labels map[string]int
	jumped bool
}

// NewMachine returns a Machine whose memory is mem, mapped at a fixed non-zero
// base so that a stray zero pointer faults instead of reading lane 0.
func NewMachine(mem []byte, lanes int) *Machine {
	return &Machine{Mem: mem, Base: 1 << 20, Lanes: lanes}
}

func (m *Machine) off(addr uint64) int {
	o := int64(addr - m.Base)
	if o < 0 || o+8 > int64(len(m.Mem)) {
		panic(fmt.Sprintf("asmgen: out of range access at %#x (base %#x, size %#x)",
			addr, m.Base, len(m.Mem)))
	}
	return int(o)
}

// Load64 reads a little-endian 64-bit word.
func (m *Machine) Load64(addr uint64) uint64 {
	o := m.off(addr)
	var v uint64
	for i := 0; i < 8; i++ {
		v |= uint64(m.Mem[o+i]) << (8 * i)
	}
	return v
}

// Store64 writes a little-endian 64-bit word.
func (m *Machine) Store64(addr, v uint64) {
	o := m.off(addr)
	for i := 0; i < 8; i++ {
		m.Mem[o+i] = byte(v >> (8 * i))
	}
}

// LoadVec reads n lanes starting at addr.
func (m *Machine) LoadVec(addr uint64, n int) [8]uint64 {
	var v [8]uint64
	for i := 0; i < n; i++ {
		v[i] = m.Load64(addr + uint64(8*i))
	}
	return v
}

// StoreVec writes n lanes starting at addr.
func (m *Machine) StoreVec(addr uint64, v [8]uint64, n int) {
	for i := 0; i < n; i++ {
		m.Store64(addr+uint64(8*i), v[i])
	}
}

func (m *Machine) setCmp(a, b uint64) { m.cmpA, m.cmpB = a, b }

// Run executes insts until it falls off the end. The step limit turns a
// mis-emitted branch into a test failure rather than a hang.
func (m *Machine) Run(insts []Inst) error {
	labels := map[string]int{}
	for i, in := range insts {
		if in.Label != "" {
			labels[in.Label] = i
		}
	}
	m.labels = labels
	const maxSteps = 200_000_000
	for m.PC = 0; m.PC < len(insts); m.PC++ {
		m.Steps++
		if m.Steps > maxSteps {
			return fmt.Errorf("asmgen: step limit exceeded (runaway loop?)")
		}
		m.jumped = false
		insts[m.PC].Sim(m)
		// A taken branch leaves PC at the label; the loop's increment then
		// lands on the instruction after it.
	}
	return nil
}

func (m *Machine) jump(label string) {
	i, ok := m.labels[label]
	if !ok {
		panic("asmgen: jump to undefined label " + label)
	}
	m.PC = i
	m.jumped = true
}
