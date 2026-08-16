// Command sweep benchmarks every input length in a range, one measurement per
// length rather than one per size class, so that cliffs and anomalies between
// the class boundaries are visible.
//
// Methodology: per length and implementation, iterations are calibrated to
// ~1ms of work, run in 9 repetitions, and the per-op time is the median of
// the repetitions. The input is read from a fixed, 64-byte-aligned buffer at
// offset 0, matching the other benchmarks.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/JohanLindvall/xxhaste"
	cespare "github.com/cespare/xxhash/v2"
	"github.com/zeebo/xxh3"
)

var sink uint64

func measure(f func([]byte) uint64, in []byte) float64 {
	// Calibrate to ~1ms.
	iters := 1000
	for {
		t := time.Now()
		for i := 0; i < iters; i++ {
			sink += f(in)
		}
		d := time.Since(t)
		if d > 200*time.Microsecond {
			iters = int(float64(iters) * float64(time.Millisecond) / float64(d))
			if iters < 1000 {
				iters = 1000
			}
			break
		}
		iters *= 4
	}
	reps := make([]float64, 9)
	for r := range reps {
		t := time.Now()
		for i := 0; i < iters; i++ {
			sink += f(in)
		}
		reps[r] = float64(time.Since(t).Nanoseconds()) / float64(iters)
	}
	sort.Float64s(reps)
	return reps[len(reps)/2]
}

func main() {
	max := flag.Int("max", 255, "largest length to measure")
	min := flag.Int("min", 0, "smallest length to measure")
	flag.Parse()

	buf := make([]byte, *max+64)
	g := uint64(2654435761)
	for i := range buf {
		buf[i] = byte(g >> 56)
		g *= 11400714785074694797
	}

	impls := []struct {
		name string
		f    func([]byte) uint64
	}{
		{"xxhaste", xxhaste.Sum64},
		{"zeebo", xxh3.Hash},
		{"cespare", cespare.Sum64},
		{"xxhaste128", func(b []byte) uint64 { return xxhaste.Sum128(b).Lo }},
		{"zeebo128", func(b []byte) uint64 { return xxh3.Hash128(b).Lo }},
	}

	// Warm up before the first measurement. A core takes a few milliseconds
	// to reach its full clock, and the first length measured is otherwise
	// charged for it: on an Apple M2 the empty input read as 12ns cold
	// against 2.4ns warm.
	for t := time.Now(); time.Since(t) < 200*time.Millisecond; {
		for _, im := range impls {
			sink += im.f(buf[:64])
		}
	}

	fmt.Print("len")
	for _, im := range impls {
		fmt.Printf(",%s", im.name)
	}
	fmt.Println()
	for n := *min; n <= *max; n++ {
		fmt.Print(n)
		for _, im := range impls {
			fmt.Printf(",%.3f", measure(im.f, buf[:n]))
		}
		fmt.Println()
		os.Stdout.Sync()
	}
}
