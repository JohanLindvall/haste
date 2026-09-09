package xxh64

import "testing"

// Exercise every small-copy size at every staged position, with unaligned
// input and a following write. Checking each prefix also checks that Sum64
// leaves the digest usable.
func TestStreamingSmallWrites(t *testing.T) {
	forEachBackend(t, func(t *testing.T) {
		for _, seed := range []uint64{0, 42} {
			for staged := 0; staged < blockLen; staged++ {
				for n := 0; n <= 2*blockLen; n++ {
					for offset := 0; offset < 8; offset++ {
						buf := testBuffer(offset + staged + n + 7)[offset:]
						d := NewSeed(seed)
						d.Write(buf[:staged])
						if n%2 == 0 {
							d.Write(buf[staged : staged+n])
						} else {
							d.WriteString(string(buf[staged : staged+n]))
						}
						if got, want := d.Sum64(), Sum64Seed(buf[:staged+n], seed); got != want {
							t.Fatalf("seed=%d staged=%d n=%d offset=%d: got %x want %x", seed, staged, n, offset, got, want)
						}
						d.Write(buf[staged+n:])
						if got, want := d.Sum64(), Sum64Seed(buf, seed); got != want {
							t.Fatal("continued digest differs")
						}
					}
				}
			}
		}
	})
}
