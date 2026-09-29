// This file carries the retry-wait primitives of
// packages/agent/src/harness/runtime/drive/retry.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A retry deadline is an absolute wall-clock instant clamped to the largest
// exactly representable JavaScript integer, so a durable not-before never
// silently loses precision.
package agentruntime

import (
	"context"
	"time"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER.
const maxSafeInteger = float64(9007199254740991)

// RetryNotBefore computes the absolute deadline for attempt based on policy.
func RetryNotBefore(policy harnesstypes.RetryPolicy, attempt int, now float64) float64 {
	sum := now + aiutils.RetryDelayMs(policy, attempt)
	if sum != float64(int64(sum)) || sum < 0 || sum > maxSafeInteger {
		return maxSafeInteger
	}
	return sum
}

// WaitUntil sleeps until the absolute millisecond deadline or the context is
// cancelled. Deadlines already in the past return immediately.
func WaitUntil(ctx context.Context, notBeforeMs float64) error {
	for {
		remaining := notBeforeMs - float64(time.Now().UnixMilli())
		if remaining <= 0 {
			return nil
		}
		if remaining > 2147483647 {
			remaining = 2147483647
		}
		timer := time.NewTimer(time.Duration(remaining) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
