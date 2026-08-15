// Command gen writes the assembly kernels and their Go declarations.
//
// It shells out to the system assembler, so it needs binutils for every target
// it generates: binutils-x86-64-linux-gnu and binutils-aarch64-linux-gnu cover
// both from either host.
//
// Usage: go generate ./...   (or: go run ./internal/asmgen/gen -out .)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/JohanLindvall/xxhaste/internal/asmgen"
)

func main() {
	out := flag.String("out", ".", "directory to write generated files to")
	only := flag.String("only", "", "generate just this backend")
	dump := flag.Int("dump", -1, "print the assembler source of kernel N (0 hashLong, 1 accumBlocks, 2 accum) instead of generating; use with -only")
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("asmgen: ")

	// -dump prints one kernel as assembler source, which is what feeds
	// llvm-mca when a backend has to be analysed rather than run.
	if *dump >= 0 {
		for _, b := range asmgen.Backends() {
			if *only != "" && b.Name != *only {
				continue
			}
			fmt.Print(asmgen.EmitAll(b.New)[*dump].Build().Text())
		}
		return
	}

	// The stubs always cover every backend of an architecture, even when only
	// one was regenerated: they have to match what dispatch calls.
	byArch := map[string][]asmgen.Backend{}
	for _, b := range asmgen.Backends() {
		byArch[b.GOARCH] = append(byArch[b.GOARCH], b)
		if *only != "" && b.Name != *only {
			continue
		}
		asm, err := asmgen.Generate(b)
		if err != nil {
			log.Fatal(err)
		}
		write(filepath.Join(*out, b.Filename()), asm)
	}
	for goarch, bs := range byArch {
		stubs, err := asmgen.GenerateStubs(goarch, bs)
		if err != nil {
			log.Fatal(err)
		}
		write(filepath.Join(*out, fmt.Sprintf("stub_%s.go", goarch)), stubs)
	}
}

func write(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", path)
}
