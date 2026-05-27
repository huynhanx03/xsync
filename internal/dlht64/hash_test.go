package dlht64

import "testing"

func TestHashUint64Distribution(t *testing.T) {
	const (
		seed = uint64(0x123456789abcdef0)
		n    = 65_536
		bins = 1_024
	)

	patterns := map[string]func(uint64) uint64{
		"sequential": func(i uint64) uint64 { return i },
		"stride20":   func(i uint64) uint64 { return i << 20 },
		"stride32":   func(i uint64) uint64 { return i << 32 },
		"highBits":   func(i uint64) uint64 { return i << 48 },
	}

	for name, pattern := range patterns {
		var counts [bins]int
		for i := uint64(0); i < n; i++ {
			counts[hashUint64(seed, pattern(i))&(bins-1)]++
		}

		used := 0
		maxCount := 0
		for _, count := range counts {
			if count == 0 {
				continue
			}
			used++
			if count > maxCount {
				maxCount = count
			}
		}
		if used != bins {
			t.Fatalf("%s used %d/%d bins", name, used, bins)
		}
		if maxCount > 2*(n/bins) {
			t.Fatalf("%s max bin count=%d, want <= %d", name, maxCount, 2*(n/bins))
		}
	}
}
