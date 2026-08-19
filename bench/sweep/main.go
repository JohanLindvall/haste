// Command sweep benchmarks every input length in a range, one measurement per
// length rather than one per size class, so that cliffs and anomalies between
// the class boundaries are visible.
//
// rapidhash has no second implementation to sit beside it here -- the only
// other one is the C reference, which cannot be called this way without cgo's
// overhead swamping a two-nanosecond hash -- so its column is read against
// itself: a cliff between two adjacent lengths is the thing this tool is for,
// and rapidhash has one every 224 bytes that no size-class benchmark would
// show. Which of them is loud is the core's choice, so read more than one: a
// Zen 4 shows 224/225 (8.56ns against 7.50) and a Redwood Cove does not
// reproduce that boundary at all, while paying 15% at 448/449 (11.70 against
// 9.94). Both are the same miss -- one byte short of another loop pass, six
// serial ladder rungs in its place.
//
// Methodology: per length and implementation, iterations are calibrated to a
// block of work -- a millisecond, or a thousand clock ticks where the clock
// is coarser than a microsecond -- run in 9 repetitions, and the per-op time
// is the median of the repetitions. The input is read from a fixed, 64-byte-aligned buffer at
// offset 0, matching the other benchmarks.
//
// Each implementation owns its whole iteration loop, so the hash inside it is
// a *direct* call that the entry point can inline into. Timing one call at a
// time through a func value instead was worth 0.65ns to haste and 0.15ns to
// zeebo/xxh3 on a Redwood Cove, which is enough to invert the comparison: it
// reported haste 9% behind over 33..64 bytes where a direct call has it 6%
// ahead. An indirection is exactly what these entry points are shaped to
// avoid, so measuring through one measures the wrong thing.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JohanLindvall/haste/rapidhash"
	"github.com/JohanLindvall/haste/xxh3"
	"github.com/JohanLindvall/haste/xxh64"
	bytedance "github.com/bytedance/gopkg/util/xxhash3"
	cespare "github.com/cespare/xxhash/v2"
	zeebo "github.com/zeebo/xxh3"
	dw1 "go.dw1.io/rapidhash"
)

var sink uint64

// runner hashes in exactly iters times, accumulating the results so nothing
// is optimized away. One per implementation, each with the hash spelled out
// so the call is direct; see the package comment.
type runner func(in []byte, iters int) uint64

// block is how long one timed run should take, and reps how many of them
// each point gets. Both come from the clock rather than from a constant.
//
// A millisecond and nine repetitions are right where time.Now resolves to
// tens of nanoseconds, which is Linux and macOS. It is not right everywhere:
// on a windows-latest runner the whole 0..255 matrix came back mostly 0.000,
// with the odd real number scattered through it, because the timed block was
// under the clock's step and most deltas read as zero.
//
// The first fix for that asked for a thousand ticks a block, which is correct
// and unusable: with a millisecond tick it is a second per repetition, nine
// per point, and four hours for the matrix. It ran for an hour on a runner
// before that became obvious. So the block asks for a hundred ticks -- one
// percent of quantization, which the median across repetitions can carry --
// capped at fifty milliseconds, and a coarse clock trades repetitions for
// the longer blocks it forces.
var block, reps = calibrate()

func calibrate() (time.Duration, int) {
	// The smallest non-zero delta the clock will admit, best of five.
	tick := time.Hour
	for i := 0; i < 5; i++ {
		t := time.Now()
		var d time.Duration
		for d == 0 {
			d = time.Since(t)
		}
		if d < tick {
			tick = d
		}
	}
	b := tick * 100
	if b > 50*time.Millisecond {
		b = 50 * time.Millisecond
	}
	if b < time.Millisecond {
		b = time.Millisecond
	}
	r := 9
	if b > 5*time.Millisecond {
		r = 3
	}
	if tick > time.Millisecond {
		fmt.Fprintf(os.Stderr, "sweep: clock ticks at %v; blocks of %v are %d ticks, so these numbers are quantized\n",
			tick, b, b/tick)
	}
	return b, r
}

func measure(f runner, in []byte) float64 {
	// Calibrate to one block.
	iters := 1000
	for {
		t := time.Now()
		sink += f(in, iters)
		d := time.Since(t)
		if d > block/5 {
			iters = int(float64(iters) * float64(block) / float64(d))
			if iters < 1000 {
				iters = 1000
			}
			break
		}
		iters *= 4
	}
	// A repetition that reads exactly zero did not take less than no time:
	// it finished inside one tick of the clock, because the calibration pass
	// above was interfered with and set iters too low. Multiplying the count
	// and starting the point again is the only honest answer, and it
	// terminates -- each attempt quadruples the work, so a clock coarse
	// enough to need many of them would have to be minutes wide.
	//
	// On a fine clock this never fires. On a windows-11-arm runner it fired
	// for 49 of 1,792 cells before it existed, which is 3% of a matrix
	// reading as hashes that cost nothing.
	took := make([]float64, reps)
	for attempt := 0; ; attempt++ {
		zero := false
		for r := range took {
			t := time.Now()
			sink += f(in, iters)
			d := time.Since(t)
			if d == 0 {
				zero = true
				break
			}
			took[r] = float64(d.Nanoseconds()) / float64(iters)
		}
		if !zero {
			break
		}
		if attempt >= 8 {
			return 0 // a clock this coarse cannot be worked around here
		}
		iters *= 4
	}
	sort.Float64s(took)

	// The median of the repetitions, unless interference stretched more than
	// half of them: a full 0..256 matrix once produced two points 3x off
	// because a burst outlasted five of the nine reps, and the median
	// faithfully reported the burst. The minimum cannot be slowed by
	// interference, only by miscalibration, so when the median disagrees
	// with it by more than 20% the run was dirty and the minimum is the
	// honest number.
	med, min := took[len(took)/2], took[0]
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

	// Each length gets its own allocation, sized to it. Slicing one buffer
	// sized to the longest length in the run made a length's result depend on
	// what else was in the run, and not equally: hashing a 16 KiB prefix of a
	// 128 KiB allocation measured cespare/xxhash 19% slower than hashing a
	// 16 KiB allocation, where this library moved 2%. That is larger than the
	// differences the sweep is read for.
	fill := func(n int) []byte {
		b := make([]byte, n)
		g := uint64(2654435761)
		for i := range b {
			b[i] = byte(g >> 56)
			g *= 11400714785074694797
		}
		return b
	}
	warm := fill(64)

	impls := []struct {
		name string
		f    runner
	}{
		{"haste-xxh3", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += xxh3.Sum64(b)
			}
			return s
		}},
		{"zeebo", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += zeebo.Hash(b)
			}
			return s
		}},
		{"bytedance", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += bytedance.Hash(b)
			}
			return s
		}},
		{"haste-xxh64", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += xxh64.Sum64(b)
			}
			return s
		}},
		{"haste-rapid", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += rapidhash.Sum64(b)
			}
			return s
		}},
		{"dw1-rapid", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += dw1.Hash(b)
			}
			return s
		}},
		{"cespare", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += cespare.Sum64(b)
			}
			return s
		}},
		{"haste-xxh3-128", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += xxh3.Sum128(b).Lo
			}
			return s
		}},
		{"zeebo128", func(b []byte, n int) uint64 {
			var s uint64
			for i := 0; i < n; i++ {
				s += zeebo.Hash128(b).Lo
			}
			return s
		}},
	}

	// Warm up before the first measurement. A core takes a few milliseconds
	// to reach its full clock, and the first length measured is otherwise
	// charged for it: on an Apple M2 the empty input read as 12ns cold
	// against 2.4ns warm.
	for t := time.Now(); time.Since(t) < 200*time.Millisecond; {
		for _, im := range impls {
			sink += im.f(warm, 1000)
		}
	}

	fmt.Print("len")
	for _, im := range impls {
		fmt.Printf(",%s", im.name)
	}
	fmt.Println()
	for _, n := range list {
		in := fill(n)
		fmt.Print(n)
		for _, im := range impls {
			fmt.Printf(",%.3f", measure(im.f, in))
		}
		fmt.Println()
		os.Stdout.Sync()
	}
}
