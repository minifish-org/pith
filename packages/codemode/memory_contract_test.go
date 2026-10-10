package codemode

import "testing"

func TestLinearMemoryCaps(t *testing.T) {
	for _, test := range []struct {
		heap  uint64
		pages uint32
	}{
		{0, 8192}, {1, 256}, {32 << 20, 2304}, {1 << 30, 65536}, {^uint64(0), 65536},
	} {
		if got := pageLimitFor(test.heap); got != test.pages {
			t.Errorf("heap %d: got %d pages, want %d", test.heap, got, test.pages)
		}
	}
}
