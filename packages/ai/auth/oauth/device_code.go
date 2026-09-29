// This file is a Go port of packages/ai/src/auth/oauth/device-code.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Device-code polling status values.
const (
	DeviceCodeStatusPending  = "pending"
	DeviceCodeStatusSlowDown = "slow_down"
	DeviceCodeStatusFailed   = "failed"
	DeviceCodeStatusComplete = "complete"
)

const (
	cancelMessage          = "Login cancelled"
	timeoutMessage         = "Device flow timed out"
	slowDownTimeoutMessage = "Device flow timed out after one or more slow_down responses. " +
		"This is often caused by clock drift in WSL or VM environments. " +
		"Please sync or restart the VM clock and try again."
	minimumIntervalMs           = 1000
	defaultPollIntervalSeconds  = 5
	slowDownIntervalIncrementMs = 5000
)

// deviceCodeNow is the polling clock in Unix milliseconds. It is replaceable in
// tests so polling can be exercised deterministically.
var deviceCodeNow = func() int64 { return time.Now().UnixMilli() }

// OAuthDeviceCodePollResult is one poll outcome. Status selects which fields
// are meaningful: "complete" carries Value, "slow_down" carries
// IntervalSeconds, "failed" carries Message.
//
// Ports `OAuthDeviceCodePollResult` from
// packages/ai/src/auth/oauth/device-code.ts.
type OAuthDeviceCodePollResult[T any] struct {
	Status          string
	IntervalSeconds *float64
	Message         string
	Value           T
}

// DeviceCodePending builds a "pending" result.
func DeviceCodePending[T any]() OAuthDeviceCodePollResult[T] {
	return OAuthDeviceCodePollResult[T]{Status: DeviceCodeStatusPending}
}

// DeviceCodeSlowDown builds a "slow_down" result.
func DeviceCodeSlowDown[T any](intervalSeconds *float64) OAuthDeviceCodePollResult[T] {
	return OAuthDeviceCodePollResult[T]{Status: DeviceCodeStatusSlowDown, IntervalSeconds: intervalSeconds}
}

// DeviceCodeFailed builds a "failed" result with a message.
func DeviceCodeFailed[T any](message string) OAuthDeviceCodePollResult[T] {
	return OAuthDeviceCodePollResult[T]{Status: DeviceCodeStatusFailed, Message: message}
}

// DeviceCodeComplete builds a "complete" result.
func DeviceCodeComplete[T any](value T) OAuthDeviceCodePollResult[T] {
	return OAuthDeviceCodePollResult[T]{Status: DeviceCodeStatusComplete, Value: value}
}

// OAuthDeviceCodePollOptions configures a device-code poll loop.
//
// Ports `OAuthDeviceCodePollOptions` from
// packages/ai/src/auth/oauth/device-code.ts.
type OAuthDeviceCodePollOptions[T any] struct {
	IntervalSeconds     *float64
	ExpiresInSeconds    *float64
	WaitBeforeFirstPoll bool
	Poll                func() (OAuthDeviceCodePollResult[T], error)
	Signal              context.Context
}

// AbortableSleep waits for ms milliseconds, rejecting with cancelMessage when
// the signal aborts.
//
// Ports `abortableSleep` from packages/ai/src/auth/oauth/device-code.ts.
func AbortableSleep(ctx context.Context, ms float64, cancelMessage string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s", cancelMessage)
	}
	if ms <= 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s", cancelMessage)
		default:
			return nil
		}
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s", cancelMessage)
	}
}

// PollOAuthDeviceCodeFlow polls until the flow completes, fails, times out or
// the signal aborts.
//
// Ports `pollOAuthDeviceCodeFlow` from
// packages/ai/src/auth/oauth/device-code.ts.
func PollOAuthDeviceCodeFlow[T any](ctx context.Context, options OAuthDeviceCodePollOptions[T]) (T, error) {
	var zero T
	if ctx == nil {
		ctx = options.Signal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Poll == nil {
		return zero, fmt.Errorf("oauth: device code poll requires a poll function")
	}

	deadline := int64(math.MaxInt64)
	if options.ExpiresInSeconds != nil {
		deadline = deviceCodeNow() + int64(*options.ExpiresInSeconds*1000)
	}
	intervalMs := float64(minimumIntervalMs)
	defaultIntervalSeconds := float64(defaultPollIntervalSeconds)
	if options.IntervalSeconds != nil {
		defaultIntervalSeconds = *options.IntervalSeconds
	}
	if floor := math.Floor(defaultIntervalSeconds * 1000); floor > intervalMs {
		intervalMs = floor
	}

	slowDownResponses := 0
	if options.WaitBeforeFirstPoll {
		remainingMs := deadline - deviceCodeNow()
		if remainingMs > 0 {
			wait := math.Min(intervalMs, float64(remainingMs))
			if err := AbortableSleep(ctx, wait, cancelMessage); err != nil {
				return zero, err
			}
		}
	}

	for deviceCodeNow() < deadline {
		if ctx.Err() != nil {
			return zero, fmt.Errorf("%s", cancelMessage)
		}

		result, err := options.Poll()
		if err != nil {
			return zero, err
		}
		if result.Status == DeviceCodeStatusComplete {
			return result.Value, nil
		}
		if result.Status == DeviceCodeStatusFailed {
			return zero, fmt.Errorf("%s", result.Message)
		}
		if result.Status == DeviceCodeStatusSlowDown {
			slowDownResponses++
			if result.IntervalSeconds != nil && !math.IsInf(*result.IntervalSeconds, 0) && !math.IsNaN(*result.IntervalSeconds) && *result.IntervalSeconds > 0 {
				intervalMs = math.Max(minimumIntervalMs, math.Floor(*result.IntervalSeconds*1000))
			} else {
				intervalMs = math.Max(minimumIntervalMs, intervalMs+slowDownIntervalIncrementMs)
			}
		}

		remainingMs := deadline - deviceCodeNow()
		if remainingMs <= 0 {
			break
		}
		wait := math.Min(intervalMs, float64(remainingMs))
		if err := AbortableSleep(ctx, wait, cancelMessage); err != nil {
			return zero, err
		}
	}

	if slowDownResponses > 0 {
		return zero, fmt.Errorf("%s", slowDownTimeoutMessage)
	}
	return zero, fmt.Errorf("%s", timeoutMessage)
}
