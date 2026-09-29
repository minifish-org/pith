package usage

import (
	"testing"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestEmptyUsage(t *testing.T) {
	got := EmptyUsage()
	if got.Input != 0 || got.Output != 0 || got.TotalTokens != 0 || got.Cost.Total != 0 {
		t.Fatalf("unexpected empty usage: %#v", got)
	}
	if got.CacheWrite1h != nil || got.Reasoning != nil {
		t.Fatalf("optional fields must be absent: %#v", got)
	}
}

func TestAddUsage(t *testing.T) {
	left := aitypes.Usage{
		Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, TotalTokens: 10,
		Cost: aitypes.UsageCost{Input: 0.1, Output: 0.2, CacheRead: 0.3, CacheWrite: 0.4, Total: 1.0},
	}
	right := aitypes.Usage{
		Input: 5, Output: 6, CacheRead: 7, CacheWrite: 8, TotalTokens: 26,
		Cost: aitypes.UsageCost{Input: 0.5, Output: 0.6, CacheRead: 0.7, CacheWrite: 0.8, Total: 2.6},
	}
	got := AddUsage(left, right)
	if got.Input != 6 || got.Output != 8 || got.CacheRead != 10 || got.CacheWrite != 12 || got.TotalTokens != 36 {
		t.Fatalf("unexpected usage totals: %#v", got)
	}
	if got.Cost.Total != 3.6 {
		t.Fatalf("unexpected cost: %#v", got.Cost)
	}
	if got.CacheWrite1h != nil || got.Reasoning != nil {
		t.Fatalf("optional fields must stay absent when both sides omit them: %#v", got)
	}
}

func TestAddUsageOptionalFields(t *testing.T) {
	one := 1.0
	three := 3.0
	left := aitypes.Usage{CacheWrite1h: &one, Reasoning: &one}
	right := aitypes.Usage{Reasoning: &three}
	got := AddUsage(left, right)
	if got.CacheWrite1h == nil || *got.CacheWrite1h != 1 {
		t.Fatalf("cacheWrite1h mismatch: %#v", got.CacheWrite1h)
	}
	if got.Reasoning == nil || *got.Reasoning != 4 {
		t.Fatalf("reasoning mismatch: %#v", got.Reasoning)
	}
}
