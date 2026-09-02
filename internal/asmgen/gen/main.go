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

	"github.com/JohanLindvall/haste/internal/asmgen"
)

func main() {
	out := flag.String("out", ".", "directory to write generated files to")
	only := flag.String("only", "", "generate just this backend")
	prefetch := flag.Int("prefetch", 0, "experimental: x86 stripe prefetch distance in bytes")
	tailSkips := flag.Int("tailskips", 0, "experimental: x86 XXH64 tail skip set (bits: 1 all, 2 words, 4 afterwords, 8 bytes)")
	dump := flag.Int("dump", -1, "print the assembler source of kernel N (0 hashLong, 1 accumBlocks, 2 accum, 3 accumBlocks2; for the XXH64 backends 0 sum64, 1 blocks) instead of generating; use with -only")
	flag.Parse()
	asmgen.PrefetchDistance = *prefetch
	asmgen.X86TailSkips = *tailSkips
	log.SetFlags(0)
	log.SetPrefix("asmgen: ")

	// -dump prints one kernel as assembler source, which is what feeds
	// llvm-mca when a backend has to be analysed rather than run.
	if *dump >= 0 {
		for _, b := range asmgen.AllBackends() {
			if *only != "" && b.Name != *only {
				continue
			}
			fmt.Print(b.EmitAll()[*dump].Build().Text())
		}
		return
	}

	// The stubs always cover every backend of a package and architecture,
	// even when only one was regenerated: they have to match what dispatch
	// calls.
	type key struct{ dir, goarch string }
	byPkg := map[key][]asmgen.Backend{}
	var keys []key
	for _, b := range asmgen.AllBackends() {
		k := key{b.Dir, b.GOARCH}
		if _, seen := byPkg[k]; !seen {
			keys = append(keys, k)
		}
		byPkg[k] = append(byPkg[k], b)
		if *only != "" && b.Name != *only {
			continue
		}
		asm, err := asmgen.Generate(b)
		if err != nil {
			log.Fatal(err)
		}
		write(filepath.Join(*out, b.Filename()), asm)
	}
	for _, k := range keys {
		stubs, err := asmgen.GenerateStubs(k.goarch, byPkg[k])
		if err != nil {
			log.Fatal(err)
		}
		write(filepath.Join(*out, k.dir, fmt.Sprintf("stub_%s.go", k.goarch)), stubs)
	}
}

func write(path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", path)
}
