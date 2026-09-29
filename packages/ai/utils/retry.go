// This file is a Go port of packages/ai/src/utils/retry.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"context"
	"regexp"

	"github.com/minifish-org/pith/packages/ai/types"
)

func buildProviderErrorPattern(patterns []string) *regexp.Regexp {
	joined := ""
	for i, pattern := range patterns {
		if i > 0 {
			joined += "|"
		}
		joined += pattern
	}
	return regexp.MustCompile("(?i)" + joined)
}

var nonRetryableProviderLimitErrorPattern = buildProviderErrorPattern([]string{
	// OpenCode Go/free-tier limits returned as 429 JSON error types by OpenCode's
	// Zen API. These are subscription/account limits, not transient throttles.
	"GoUsageLimitError",
	"FreeUsageLimitError",

	// OpenCode Go subscription-limit text asks users to enable available-balance
	// usage after rolling/weekly/monthly limits are reached.
	"Monthly usage limit reached",
	"available balance",

	// Generic quota/budget/billing exhaustion. `insufficient_quota` is OpenAI's
	// quota/billing error code; the other strings cover common gateway wording.
	"insufficient_quota",
	"out of budget",
	"quota exceeded",
	"billing",
})

var retryableProviderErrorPattern = buildProviderErrorPattern([]string{
	// Generic provider load, HTTP status, and server-side transient failures.
	"overloaded",
	"currently experiencing high demand",
	"rate.?limit",
	"too many requests",
	"429",
	"500",
	"502",
	"503",
	"504",
	"520",
	"524",
	"service.?unavailable",
	"server.?error",
	"internal.?error",

	// Wrapper/provider text for transient upstream failures, including OpenRouter
	// "Provider returned error" responses (#2264).
	"provider.?returned.?error",
	"exceeded request buffer limit while retrying upstream",

	// Network, proxy, and fetch transport failures. This includes OpenAI Codex
	// raw-fetch failures such as "upstream connect", "connection refused", and
	// "reset before headers" (#733), plus OpenRouter connection drops (#3317).
	"network.?error",
	"connection.?error",
	"connection.?refused",
	"connection.?lost",
	"other side closed",
	"fetch failed",
	"getaddrinfo",
	"ENOTFOUND",
	"EAI_AGAIN",
	"upstream.?connect",
	"reset before headers",
	"socket hang up",
	"socket connection was closed",
	"timed? out",
	"timeout",
	"terminated",

	// WebSocket transports can report close/error text instead of HTTP/fetch text.
	"websocket.?closed",
	"websocket.?error",

	// Premature stream endings from SDKs and transports.
	"ended without",
	"stream ended before message_stop",
	"stream ended before a terminal response event",
	"http2 request did not get a response",

	// Provider-requested retry delay cap failures should flow through the outer
	// retry policy so callers can surface/abort the backoff (#1123).
	"retry delay",

	// Explicit retry guidance emitted mid-stream by OpenAI Responses and Bedrock
	// stream exceptions (#6019).
	"you can retry your request",
	"try your request again",
	"please retry your request",

	// gRPC based providers (e.g. NVIDIA NIM)
	"ResourceExhausted",
})

// RetryPolicy is a bounded-attempt exponential-backoff retry policy.
// Per-attempt delay is `baseDelayMs * 2^(attempt-1)` before the cap.
type RetryPolicy struct {
	Enabled bool
	// MaxRetries is the maximum number of retry attempts (0 = no retries). The
	// initial call never counts as a retry.
	MaxRetries int
	// BaseDelayMs is the base delay in milliseconds.
	BaseDelayMs float64
	// MaxAgentDelayMs caps an agent-level retry delay. A nil value defaults to
	// DefaultMaxAgentRetryDelayMs.
	MaxAgentDelayMs *float64
}

// DefaultMaxAgentRetryDelayMs is the default cap for agent-level retry delays.
const DefaultMaxAgentRetryDelayMs = 60000.0

// RetryDelayMs computes the capped exponential backoff delay for an attempt.
func RetryDelayMs(policy RetryPolicy, attempt int) float64 {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	delay := policy.BaseDelayMs * pow2(exponent)
	safeDelay := delay
	if !isSafeInteger(safeDelay) {
		safeDelay = maxSafeInteger
	}
	maxDelay := DefaultMaxAgentRetryDelayMs
	if policy.MaxAgentDelayMs != nil {
		maxDelay = *policy.MaxAgentDelayMs
	}
	if safeDelay < maxDelay {
		return safeDelay
	}
	return maxDelay
}

const maxSafeInteger = float64(9007199254740991)

func isSafeInteger(value float64) bool {
	return value == float64(int64(value)) && value >= -maxSafeInteger && value <= maxSafeInteger
}

func pow2(exponent int) float64 {
	result := 1.0
	for i := 0; i < exponent; i++ {
		result *= 2
	}
	return result
}

// RetryCallbacks are optional callbacks emitted by RetryAssistantCall around
// each retry. A callback may return an error to abort the loop.
type RetryCallbacks struct {
	// OnRetryScheduled runs before the backoff sleep of each retry attempt
	// (1-indexed).
	OnRetryScheduled func(attempt, maxAttempts int, delayMs float64, errorMessage string) error
	// OnRetryAttemptStart runs after the backoff sleep, immediately before the
	// retried call starts.
	OnRetryAttemptStart func() error
	// OnRetryFinished runs once when the loop ends: success if a later call
	// completed normally.
	OnRetryFinished func(success bool, attempt int, finalError string) error
}

// RetryAssistantCall runs a single assistant-producing call with bounded retry
// on transient errors.
//
// Behavior:
//   - A successful response is returned immediately. Aborts are terminal and
//     never retried, but reported as unsuccessful if they happen after a retry
//     was scheduled. Aborts during the backoff sleep normalize to an aborted
//     AssistantMessage too.
//   - A non-retryable error is returned immediately so deterministic errors fail
//     fast.
//   - Otherwise the call retries up to MaxRetries times with exponential
//     backoff.
//
// When policy is nil or disabled, the first response is returned unchanged.
func RetryAssistantCall(produce func() (types.AssistantMessage, error), policy *RetryPolicy, ctx context.Context, callbacks *RetryCallbacks) (types.AssistantMessage, error) {
	maxAttempts := 0
	if policy != nil && policy.Enabled {
		maxAttempts = policy.MaxRetries
	}

	attempt := 0
	lastRetryAttempt := 0
	lastRetryMessage := ""
	hasLastRetry := false

	for {
		response, err := produce()
		if err != nil {
			if hasLastRetry {
				reportRetryFinished(callbacks, false, lastRetryAttempt, "")
			}
			var zero types.AssistantMessage
			return zero, err
		}

		// Abort: terminal but not successful. Never retry an aborted message.
		if response.StopReason == types.StopReasonAborted {
			if hasLastRetry {
				reportRetryFinished(callbacks, false, lastRetryAttempt, "")
			}
			return response, nil
		}

		// Success: non-error, non-abort responses return as-is.
		if response.StopReason != types.StopReasonError {
			if hasLastRetry {
				reportRetryFinished(callbacks, true, lastRetryAttempt, "")
			}
			return response, nil
		}

		// Non-retryable, or budget exhausted: return the final error message.
		if attempt >= maxAttempts || !IsRetryableAssistantError(response) {
			if hasLastRetry {
				finalError := ""
				if response.ErrorMessage != nil {
					finalError = *response.ErrorMessage
				}
				reportRetryFinished(callbacks, false, lastRetryAttempt, finalError)
			}
			return response, nil
		}

		attempt++
		lastRetryAttempt = attempt
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			lastRetryMessage = *response.ErrorMessage
		} else {
			lastRetryMessage = "Unknown error"
		}
		hasLastRetry = true
		delayMs := RetryDelayMs(*policy, attempt)
		if callbacks != nil && callbacks.OnRetryScheduled != nil {
			if err := callbacks.OnRetryScheduled(attempt, maxAttempts, delayMs, lastRetryMessage); err != nil {
				return response, err
			}
		}

		// Normalize aborts during retry backoff to the same AssistantMessage
		// shape as provider stream aborts.
		if sleepErr := Sleep(ctx, delayMs); sleepErr != nil {
			reportRetryFinished(callbacks, false, attempt, lastRetryMessage)
			if ctx != nil && ctx.Err() != nil {
				aborted := response
				aborted.StopReason = types.StopReasonAborted
				aborted.ErrorMessage = nil
				return aborted, nil
			}
			return response, sleepErr
		}
		if callbacks != nil && callbacks.OnRetryAttemptStart != nil {
			if err := callbacks.OnRetryAttemptStart(); err != nil {
				return response, err
			}
		}
	}
}

func reportRetryFinished(callbacks *RetryCallbacks, success bool, attempt int, finalError string) {
	if callbacks != nil && callbacks.OnRetryFinished != nil {
		_ = callbacks.OnRetryFinished(success, attempt, finalError)
	}
}

// IsRetryableAssistantError classifies whether a failed assistant message looks
// like a transient provider or transport error. This does not implement retry
// policy; callers should handle context overflow separately, then apply their
// own retry budget, backoff, and reporting before restarting the assistant turn.
func IsRetryableAssistantError(message types.AssistantMessage) bool {
	if message.StopReason != types.StopReasonError || message.ErrorMessage == nil {
		return false
	}
	errorMessage := *message.ErrorMessage
	if nonRetryableProviderLimitErrorPattern.MatchString(errorMessage) {
		return false
	}
	return retryableProviderErrorPattern.MatchString(errorMessage)
}
