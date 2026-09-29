// Package harnessconfig is the Go port of
// packages/agent/src/harness/config.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package validates harness configuration before it is installed. Validation
// functions return an error instead of throwing, but keep the upstream
// classification: duplicate tool names are a type error and numeric policy
// violations are range errors.
package harnessconfig

import (
	"fmt"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// DefaultRetryPolicy is the retry policy used when none is supplied.
var DefaultRetryPolicy = aiutils.RetryPolicy{
	Enabled:         true,
	MaxRetries:      3,
	BaseDelayMs:     1_000,
	MaxAgentDelayMs: floatPointer(aiutils.DefaultMaxAgentRetryDelayMs),
}

func floatPointer(value float64) *float64 { return &value }

// DuplicateToolNameError reports a repeated tool name.
type DuplicateToolNameError struct {
	Name string
}

func (e *DuplicateToolNameError) Error() string {
	return fmt.Sprintf("Duplicate tool name: %q", e.Name)
}

// ValidateToolNames rejects repeated tool names.
func ValidateToolNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			return &DuplicateToolNameError{Name: name}
		}
		seen[name] = struct{}{}
	}
	return nil
}

// RetryPolicyRangeError reports a retry-policy value outside the accepted range.
type RetryPolicyRangeError struct{}

func (e *RetryPolicyRangeError) Error() string {
	return "Retry policy values must be finite non-negative safe integers"
}

// CompactionSettingsRangeError reports a compaction token count outside the
// accepted range.
type CompactionSettingsRangeError struct{}

func (e *CompactionSettingsRangeError) Error() string {
	return "Compaction token counts must be finite non-negative safe integers"
}

// ValidateRetryPolicy rejects unsafe or negative retry policy numbers.
func ValidateRetryPolicy(policy aiutils.RetryPolicy) error {
	if !isSafeInteger(int64(policy.MaxRetries)) || policy.MaxRetries < 0 || int64(policy.MaxRetries) == maxSafeInteger {
		return &RetryPolicyRangeError{}
	}
	if !isSafeNumber(policy.BaseDelayMs) || policy.BaseDelayMs < 0 {
		return &RetryPolicyRangeError{}
	}
	if policy.MaxAgentDelayMs != nil {
		if !isSafeNumber(*policy.MaxAgentDelayMs) || *policy.MaxAgentDelayMs < 0 {
			return &RetryPolicyRangeError{}
		}
	}
	return nil
}

// ValidateCompactionSettings rejects unsafe or negative token counts.
func ValidateCompactionSettings(settings harnesstypes.CompactionSettings) error {
	if settings.ReserveTokens < 0 || settings.KeepRecentTokens < 0 {
		return &CompactionSettingsRangeError{}
	}
	return nil
}

const maxSafeInteger = int64(9007199254740991)

func isSafeInteger(value int64) bool {
	return value >= -maxSafeInteger && value <= maxSafeInteger
}

func isSafeNumber(value float64) bool {
	if value != value { // NaN
		return false
	}
	if value > float64(maxSafeInteger) || value < -float64(maxSafeInteger) {
		return false
	}
	return value == float64(int64(value))
}
