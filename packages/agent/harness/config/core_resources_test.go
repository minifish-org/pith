package harnessconfig

import (
	"errors"
	"testing"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

func TestDefaultRetryPolicy(t *testing.T) {
	if !DefaultRetryPolicy.Enabled || DefaultRetryPolicy.MaxRetries != 3 || DefaultRetryPolicy.BaseDelayMs != 1000 {
		t.Fatalf("unexpected default retry policy: %#v", DefaultRetryPolicy)
	}
	if DefaultRetryPolicy.MaxAgentDelayMs == nil || *DefaultRetryPolicy.MaxAgentDelayMs != aiutils.DefaultMaxAgentRetryDelayMs {
		t.Fatalf("unexpected max agent delay: %#v", DefaultRetryPolicy.MaxAgentDelayMs)
	}
}

func TestValidateToolNames(t *testing.T) {
	if err := ValidateToolNames([]string{"a", "b"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := ValidateToolNames([]string{"a", "b", "a"})
	var duplicate *DuplicateToolNameError
	if !errors.As(err, &duplicate) || duplicate.Name != "a" {
		t.Fatalf("expected duplicate tool error, got %v", err)
	}
}

func TestValidateRetryPolicy(t *testing.T) {
	valid := aiutils.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 1000}
	if err := ValidateRetryPolicy(valid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	negative := valid
	negative.MaxRetries = -1
	if err := ValidateRetryPolicy(negative); err == nil {
		t.Fatal("expected negative retries to fail")
	}
	unsafe := valid
	unsafe.BaseDelayMs = 1.5
	if err := ValidateRetryPolicy(unsafe); err == nil {
		t.Fatal("expected fractional base delay to fail")
	}
	unsafeAgent := valid
	value := -1.0
	unsafeAgent.MaxAgentDelayMs = &value
	if err := ValidateRetryPolicy(unsafeAgent); err == nil {
		t.Fatal("expected negative max agent delay to fail")
	}
}

func TestValidateCompactionSettings(t *testing.T) {
	if err := ValidateCompactionSettings(harnesstypes.CompactionSettings{Enabled: true, ReserveTokens: 1, KeepRecentTokens: 2}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateCompactionSettings(harnesstypes.CompactionSettings{ReserveTokens: -1}); err == nil {
		t.Fatal("expected negative reserve tokens to fail")
	}
	if err := ValidateCompactionSettings(harnesstypes.CompactionSettings{KeepRecentTokens: -1}); err == nil {
		t.Fatal("expected negative keep recent tokens to fail")
	}
}
