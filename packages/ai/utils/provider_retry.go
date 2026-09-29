// This file is a Go port of packages/ai/src/utils/provider-retry.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultMaxRetryDelayMs = 60000.0

// ProviderRetryOptions are the retry options for a provider request.
type ProviderRetryOptions struct {
	MaxRetries *int
	// MaxRetryDelayMs caps a provider-requested retry delay. Zero disables the
	// cap. A nil value defaults to 60 seconds.
	MaxRetryDelayMs *float64
	// Signal cancels the request and any backoff sleep.
	Signal context.Context
}

// ProviderError is the SDK error surface probed for retryability.
type ProviderError struct {
	Err     error
	Status  *int
	Headers http.Header
}

// Error implements error.
func (e *ProviderError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "provider error"
}

// AsProviderError reports whether an error is a provider error.
func AsProviderError(err error) (*ProviderError, bool) {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr, true
	}
	return nil, false
}

// isRetryableProviderError mirrors the pinned OpenAI/Anthropic SDK retry policy;
// review when either SDK is upgraded.
func isRetryableProviderError(err *ProviderError) bool {
	if err.Headers != nil {
		switch err.Headers.Get("x-should-retry") {
		case "true":
			return true
		case "false":
			return false
		}
	}
	if err.Status == nil {
		return true
	}
	status := *err.Status
	return status == 408 || status == 409 || status == 429 || status >= 500
}

func validateServerRetryDelayMs(delayMs float64, maxRetryDelayMs *float64, providerErrorMessage string) (float64, error) {
	maxDelayMs := defaultMaxRetryDelayMs
	if maxRetryDelayMs != nil {
		maxDelayMs = *maxRetryDelayMs
	}
	if maxDelayMs > 0 && delayMs > maxDelayMs {
		return 0, fmt.Errorf("Server requested %ds retry delay (max: %ds). %s",
			int(math.Ceil(delayMs/1000)), int(math.Ceil(maxDelayMs/1000)), providerErrorMessage)
	}
	return delayMs, nil
}

func getRetryDelayMs(err *ProviderError, retryIndex int, maxRetryDelayMs *float64) (float64, error) {
	if err.Headers != nil {
		if retryAfterMs := err.Headers.Get("retry-after-ms"); retryAfterMs != "" {
			if value, parseErr := strconv.ParseFloat(strings.TrimSpace(retryAfterMs), 64); parseErr == nil {
				return validateServerRetryDelayMs(value, maxRetryDelayMs, err.Error())
			}
		}
		if retryAfter := err.Headers.Get("retry-after"); retryAfter != "" {
			seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(retryAfter), 64)
			delayMs := 0.0
			if parseErr != nil {
				if parsedTime, timeErr := http.ParseTime(retryAfter); timeErr == nil {
					delayMs = float64(parsedTime.UnixMilli() - time.Now().UnixMilli())
				}
			} else {
				delayMs = seconds * 1000
			}
			return validateServerRetryDelayMs(delayMs, maxRetryDelayMs, err.Error())
		}
	}

	exponentialDelay := math.Min(0.5*math.Pow(2, float64(retryIndex)), 8) * 1000
	return exponentialDelay * (1 - rand.Float64()*0.25), nil
}

// AbortProviderRequestError reports an aborted provider request. Callers can
// test with errors.Is(err, ErrAbortProviderRequest).
var ErrAbortProviderRequest = errors.New("Request aborted")

func abortableSleep(ctx context.Context, ms float64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return ErrAbortProviderRequest
	}
	if ms < 0 {
		ms = 0
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ErrAbortProviderRequest
	}
}

// RetryProviderRequest reproduces the retry behavior used by the OpenAI and
// Anthropic SDKs while making their backoff sleep interruptible. Provider-
// requested delays above MaxRetryDelayMs fail immediately (60 seconds by
// default); set it to zero to disable the limit.
func RetryProviderRequest[T any](ctx context.Context, request func() (T, error), options ProviderRetryOptions) (T, error) {
	maxRetries := 0
	if options.MaxRetries != nil {
		maxRetries = *options.MaxRetries
	}
	retriesRemaining := maxRetries
	signal := options.Signal
	if signal == nil {
		signal = ctx
	}

	for {
		result, err := request()
		if err == nil {
			return result, nil
		}
		if signal != nil && signal.Err() != nil {
			var zero T
			return zero, ErrAbortProviderRequest
		}
		providerErr, ok := AsProviderError(err)
		if retriesRemaining <= 0 || !ok || !isRetryableProviderError(providerErr) {
			var zero T
			return zero, err
		}

		retryIndex := maxRetries - retriesRemaining
		retriesRemaining--
		delay, delayErr := getRetryDelayMs(providerErr, retryIndex, options.MaxRetryDelayMs)
		if delayErr != nil {
			var zero T
			return zero, delayErr
		}
		if sleepErr := abortableSleep(signal, delay); sleepErr != nil {
			var zero T
			return zero, sleepErr
		}
	}
}
