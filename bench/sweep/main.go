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
	"strconv"
	"strings"
	"time"

	"github.com/JohanLindvall/xxhaste"
	"github.com/JohanLindvall/xxhaste/xxh64"
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

	// The median of the repetitions, unless interference stretched more than
	// half of them: a full 0..256 matrix once produced two points 3x off
	// because a burst outlasted five of the nine reps, and the median
	// faithfully reported the burst. The minimum cannot be slowed by
	// interference, only by miscalibration, so when the median disagrees
	// with it by more than 20% the run was dirty and the minimum is the
	// honest number.
	med, min := reps[len(reps)/2], reps[0]
	if med > min*1.2 {
		return min
	}
	return med
}

func main() {
	max := flag.Int("max", 255, "largest length to measure")
	min := flag.Int("min", 0, "smallest length to measure")
	lens := flag.String("lens", "", "comma-separated lengths to measure instead of min..max")
	flag.Parse()

	var list []int
	if *lens != "" {
		for _, f := range strings.Split(*lens, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				panic(err)
			}
			list = append(list, n)
		}
		sort.Ints(list)
		*max = list[len(list)-1]
	} else {
		for n := *min; n <= *max; n++ {
			list = append(list, n)
		}
	}

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
		{"xxhaste64", xxh64.Sum64},
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
	for _, n := range list {
		fmt.Print(n)
		for _, im := range impls {
			fmt.Printf(",%.3f", measure(im.f, buf[:n]))
		}
		fmt.Println()
		os.Stdout.Sync()
	}
}
