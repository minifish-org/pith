// Package usage is the Go port of
// packages/agent/src/harness/utils/usage.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package usage

import aitypes "github.com/minifish-org/pith/packages/ai/types"

// EmptyUsage returns a zero usage record with zeroed costs.
func EmptyUsage() aitypes.Usage {
	return aitypes.Usage{
		Input:       0,
		Output:      0,
		CacheRead:   0,
		CacheWrite:  0,
		TotalTokens: 0,
		Cost:        aitypes.UsageCost{},
	}
}

// AddUsage adds two usage records.
//
// Optional breakdown fields stay absent when both operands leave them absent;
// when either side reports one it is summed with the missing side as zero. This
// preserves the upstream spread semantics.
func AddUsage(left, right aitypes.Usage) aitypes.Usage {
	out := aitypes.Usage{
		Input:       left.Input + right.Input,
		Output:      left.Output + right.Output,
		CacheRead:   left.CacheRead + right.CacheRead,
		CacheWrite:  left.CacheWrite + right.CacheWrite,
		TotalTokens: left.TotalTokens + right.TotalTokens,
		Cost: aitypes.UsageCost{
			Input:      left.Cost.Input + right.Cost.Input,
			Output:     left.Cost.Output + right.Cost.Output,
			CacheRead:  left.Cost.CacheRead + right.Cost.CacheRead,
			CacheWrite: left.Cost.CacheWrite + right.Cost.CacheWrite,
			Total:      left.Cost.Total + right.Cost.Total,
		},
	}
	if left.CacheWrite1h != nil || right.CacheWrite1h != nil {
		total := floatPointerValue(left.CacheWrite1h) + floatPointerValue(right.CacheWrite1h)
		out.CacheWrite1h = &total
	}
	if left.Reasoning != nil || right.Reasoning != nil {
		total := floatPointerValue(left.Reasoning) + floatPointerValue(right.Reasoning)
		out.Reasoning = &total
	}
	return out
}

func floatPointerValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
