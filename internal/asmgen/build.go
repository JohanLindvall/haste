package asmgen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

// This is the "pass it to a native assembler" half of the generator. The
// instruction stream is written out as GNU assembler source, assembled, and
// disassembled again; the resulting bytes become BYTE/WORD directives in a Go
// assembly file, each carrying its own disassembly as a comment.
//
// Going through a real assembler is what makes AVX-512 and SVE2 usable here at
// all: Go's assembler knows neither the SVE encodings nor every EVEX form, and
// hand-encoding them would put the one thing no test can check — the bytes —
// under human control.

// Encoded is one assembled instruction.
type Encoded struct {
	Addr  int
	Bytes []byte
	Text  string
}

// archDirective is the .arch line each target needs before its instructions.
func archDirective(goarch string) string {
	if goarch == "arm64" {
		// armv8.6-a covers SVE2 and everything NEON; the generated code is
		// still gated at run time on the CPU actually having it.
		return ".arch armv8.6-a+sve2\n"
	}
	return ""
}

// tool returns the binary that handles goarch, preferring the native one.
func tool(goarch, name string) (string, error) {
	var candidates []string
	if runtime.GOARCH == goarch {
		candidates = append(candidates, name)
	}
	switch goarch {
	case "amd64":
		candidates = append(candidates, "x86_64-linux-gnu-"+name)
	case "arm64":
		candidates = append(candidates, "aarch64-linux-gnu-"+name)
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s for %s in PATH (tried %s); on Debian/Ubuntu install binutils-%s-linux-gnu",
		name, goarch, strings.Join(candidates, ", "),
		map[string]string{"amd64": "x86-64", "arm64": "aarch64"}[goarch])
}

// Assemble runs body through the system assembler for goarch and returns the
// encoding of each instruction.
func Assemble(goarch, body string) ([]Encoded, error) {
	dir, err := os.MkdirTemp("", "asmgen")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "k.s")
	obj := filepath.Join(dir, "k.o")
	text := archDirective(goarch) + ".text\n.globl kernel\nkernel:\n" + body
	if err := os.WriteFile(src, []byte(text), 0o644); err != nil {
		return nil, err
	}

	as, err := tool(goarch, "as")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(as, "-o", obj, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s: %v\n%s\nsource:\n%s", as, err, out, numbered(text))
	}

	objdump, err := tool(goarch, "objdump")
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(objdump, "-d", "--insn-width=16", obj).Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v", objdump, err)
	}
	return parseObjdump(string(out))
}

func numbered(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		fmt.Fprintf(&b, "%4d %s\n", i+1, line)
	}
	return b.String()
}

// parseObjdump reads the "addr:\tbytes\tmnemonic" lines of a disassembly.
func parseObjdump(s string) ([]Encoded, error) {
	var out []Encoded
	for _, line := range strings.Split(s, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		addrStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[0]), ":"))
		if !strings.HasSuffix(strings.TrimSpace(parts[0]), ":") {
			continue
		}
		addr, err := strconv.ParseInt(addrStr, 16, 64)
		if err != nil {
			continue
		}
		raw := strings.Fields(parts[1])
		var enc []byte
		for _, f := range raw {
			// x86 prints one byte per field, arm64 one 32-bit word.
			switch len(f) {
			case 2:
				v, err := strconv.ParseUint(f, 16, 8)
				if err != nil {
					return nil, fmt.Errorf("bad byte %q in %q", f, line)
				}
				enc = append(enc, byte(v))
			case 8:
				v, err := strconv.ParseUint(f, 16, 32)
				if err != nil {
					return nil, fmt.Errorf("bad word %q in %q", f, line)
				}
				enc = append(enc, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
			default:
				return nil, fmt.Errorf("unexpected encoding field %q in %q", f, line)
			}
		}
		if len(enc) == 0 {
			continue
		}
		text := strings.Join(parts[2:], " ")
		text = strings.Join(strings.Fields(text), " ")
		out = append(out, Encoded{Addr: int(addr), Bytes: enc, Text: text})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no instructions found in disassembly")
	}
	return out, nil
}

// goDirective renders one instruction as Go assembly. arm64 instructions are
// always one 32-bit word, which WORD emits directly; x86 instructions are
// variable-length, so they go out a byte at a time.
func goDirective(goarch string, e Encoded) string {
	if goarch == "arm64" {
		w := uint32(e.Bytes[0]) | uint32(e.Bytes[1])<<8 | uint32(e.Bytes[2])<<16 | uint32(e.Bytes[3])<<24
		return fmt.Sprintf("WORD $0x%08x", w)
	}
	parts := make([]string, len(e.Bytes))
	for i, b := range e.Bytes {
		parts[i] = fmt.Sprintf("BYTE $0x%02x", b)
	}
	return strings.Join(parts, "; ")
}

// Function is one generated kernel, ready to be rendered.
type Function struct {
	Def      FuncDef
	Prologue []string // Go assembly that lands the arguments in registers
	Lines    []string // the generated body, without the RET
}

// Decl renders the Go declaration of a kernel.
func (f Function) Decl() string {
	types := map[string]string{
		"acc": "*[8]uint64", "in": "unsafe.Pointer", "sec": "unsafe.Pointer",
		"n": "int", "nbStripes": "int", "secretLimit": "int", "soFar": "int",
	}
	var args []string
	for _, a := range f.Def.Args {
		t, ok := types[a]
		if !ok {
			panic("asmgen: no type for argument " + a)
		}
		args = append(args, a+" "+t)
	}
	return fmt.Sprintf("%s(%s)", f.Def.Name, strings.Join(args, ", "))
}

// ArgSize is the size of the argument area, which every kernel passes on the
// stack: these are ABI0 functions.
func (f Function) ArgSize() int { return 8 * len(f.Def.Args) }

// prologue loads the stack arguments into the registers the body expects.
func prologue(a Arch, def FuncDef) []string {
	mov := "MOVQ"
	if a.GOARCH() == "arm64" {
		mov = "MOVD"
	}
	var out []string
	for i, name := range def.Args {
		reg := goRegName(a, a.ArgGPR(i))
		out = append(out, fmt.Sprintf("%s %s+%d(FP), %s", mov, name, 8*i, reg))
	}
	return out
}

// goRegName maps a register to the name Go's assembler uses for it, which
// differs from the GNU one.
func goRegName(a Arch, r GPR) string {
	if a.GOARCH() == "arm64" {
		return fmt.Sprintf("R%d", int(r))
	}
	return map[GPR]string{
		rAX: "AX", rCX: "CX", rDX: "DX", rBX: "BX", rSI: "SI", rDI: "DI",
		r8: "R8", r9: "R9", r10: "R10", r11: "R11", r12: "R12", r13: "R13",
	}[r]
}

// Generate assembles one backend's kernels and returns the rendered .s file.
func Generate(b Backend) (asm string, err error) {
	defs := Funcs(b.Suffix)
	archs := EmitAll(b.New)
	var funcs []Function
	for i, a := range archs {
		enc, err := Assemble(a.GOARCH(), a.Build().Text())
		if err != nil {
			return "", fmt.Errorf("%s.%s: %w", b.Name, defs[i].Name, err)
		}
		lines, err := renderBody(a, enc)
		if err != nil {
			return "", fmt.Errorf("%s.%s: %w", b.Name, defs[i].Name, err)
		}
		funcs = append(funcs, Function{Def: defs[i], Prologue: prologue(a, defs[i]), Lines: lines})
	}
	var buf bytes.Buffer
	err = asmTemplate.Execute(&buf, struct {
		Backend string
		GOARCH  string
		Funcs   []Function
	}{b.Name, b.GOARCH, funcs})
	return buf.String(), err
}

// renderBody pairs each emitted instruction with its encoding. They must
// correspond one to one: an emitter that expanded into two instructions would
// desynchronize the labels, so a mismatch is a generator bug.
func renderBody(a Arch, enc []Encoded) ([]string, error) {
	var out []string
	i := 0
	for _, in := range a.Build().Insts() {
		if in.Label != "" {
			out = append(out, fmt.Sprintf("// %s:", in.Label))
			continue
		}
		if i >= len(enc) {
			return nil, fmt.Errorf("ran out of encodings at %q", in.Text)
		}
		e := enc[i]
		i++
		out = append(out, fmt.Sprintf("%s // %s", goDirective(a.GOARCH(), e), e.Text))
	}
	if i != len(enc) {
		return nil, fmt.Errorf("assembler produced %d instructions for %d emitted", len(enc), i)
	}
	return out, nil
}

var asmTemplate = template.Must(template.New("asm").Parse(
	`// Code generated by internal/asmgen. DO NOT EDIT.
//
// {{.Backend}} kernels for {{.GOARCH}}. Each line is one instruction: the
// bytes were produced by the system assembler from the generator's output,
// and the comment is the disassembly of exactly those bytes.

//go:build !purego

#include "textflag.h"
{{range .Funcs}}
// func {{.Decl}}
//
// {{.Def.Doc}}.
TEXT ·{{.Def.Name}}(SB), NOSPLIT, $0-{{.ArgSize}}
{{- range .Prologue}}
	{{.}}
{{- end}}
{{- range .Lines}}
	{{.}}
{{- end}}
	RET
{{end}}`))

// GenerateStubs renders the Go declarations for a set of backends on one
// architecture. They are separate from the .s files so that go vet can check
// each kernel's argument offsets against the signature it is called with.
func GenerateStubs(goarch string, backends []Backend) (string, error) {
	var funcs []Function
	for _, b := range backends {
		for _, d := range Funcs(b.Suffix) {
			funcs = append(funcs, Function{Def: d})
		}
	}
	var buf bytes.Buffer
	err := stubTemplate.Execute(&buf, struct {
		GOARCH string
		Funcs  []Function
	}{goarch, funcs})
	return buf.String(), err
}

var stubTemplate = template.Must(template.New("stub").Parse(
	`// Code generated by internal/asmgen. DO NOT EDIT.

//go:build !purego

package xxhaste

import "unsafe"

// Declarations for the {{.GOARCH}} kernels in the .s files beside this one.
// Which of them runs is decided in dispatch_{{.GOARCH}}.go.
{{range .Funcs}}
// {{.Def.Name}} {{.Def.Doc}}.
//
//go:noescape
func {{.Decl}}
{{end}}`))
