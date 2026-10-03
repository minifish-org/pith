package durabletesting_test

import (
	"context"
	"errors"
	"testing"

	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
	durabletesting "github.com/minifish-org/pith/packages/durable/testing"
)

func memoryProvider() durabletesting.StorageProvider {
	return func(ctx context.Context, use func(durable.Storage) error) error {
		storage := memory.New()
		defer func() { _ = storage.Close(context.Background()) }()
		return use(storage)
	}
}

func TestCreateStorageConformanceCaseNames(t *testing.T) {
	cases := durabletesting.CreateStorageConformance(memoryProvider())
	if len(cases) != 23 {
		t.Fatalf("case count = %d want 23", len(cases))
	}
	seen := map[string]bool{}
	for _, testCase := range cases {
		if seen[testCase.Name] {
			t.Fatalf("duplicate case %q", testCase.Name)
		}
		seen[testCase.Name] = true
		if testCase.Run == nil {
			t.Fatalf("case %q has no run function", testCase.Name)
		}
	}
	expected := []string{
		"reserves ID 1 for the immutable root conversation",
		"streams long document tails across root replacement deltas",
		"rejects every operation after close",
	}
	for _, name := range expected {
		if !seen[name] {
			t.Fatalf("missing case %q", name)
		}
	}
}

// TestStorageConformanceMemory runs every source conformance case against the
// in-memory reference adapter through the shared provider contract.
func TestStorageConformanceMemory(t *testing.T) {
	durabletesting.RegisterStorageConformance(t, "memory", memoryProvider())
}

func TestNativeAssertions(t *testing.T) {
	assertions := durabletesting.NativeAssertions()
	if err := assertions.OK(true, "ok"); err != nil {
		t.Fatalf("ok: %v", err)
	}
	if err := assertions.OK(false, "boom"); err == nil {
		t.Fatal("expected ok failure")
	}
	if err := assertions.StrictEqual(2, 2); err != nil {
		t.Fatalf("strictEqual: %v", err)
	}
	if err := assertions.DeepEqual(map[string]any{"a": []any{1, 2}}, map[string]any{"a": []any{1, 2}}); err != nil {
		t.Fatalf("deepEqual: %v", err)
	}
	if err := assertions.DeepEqual([]int{1, 2}, []int{1, 3}); err == nil {
		t.Fatal("expected deepEqual failure")
	}
	if err := assertions.PartialDeepEqual(map[string]any{"a": 1, "b": 2}, map[string]any{"a": 1}); err != nil {
		t.Fatalf("partialDeepEqual: %v", err)
	}
	if err := assertions.PartialDeepEqual(map[string]any{"a": 1}, map[string]any{"a": 1, "b": 2}); err == nil {
		t.Fatal("expected partialDeepEqual failure")
	}
	if err := assertions.GreaterThan(3, 2); err != nil {
		t.Fatalf("greaterThan: %v", err)
	}
	if err := assertions.GreaterThan(2, 2); err == nil {
		t.Fatal("expected greaterThan failure")
	}
	if err := assertions.Rejects(errors.New("ID 1 already belongs to conversation"), "already belongs"); err != nil {
		t.Fatalf("rejects: %v", err)
	}
	if err := assertions.Rejects(nil, "x"); err == nil {
		t.Fatal("expected rejects failure for nil error")
	}
}

// TestStorageProviderCalledOncePerCase guards the source contract that the
// provider opens a fresh adapter exactly once for every case.
func TestStorageProviderCalledOncePerCase(t *testing.T) {
	calls := 0
	provider := func(ctx context.Context, use func(durable.Storage) error) error {
		calls++
		storage := memory.New()
		defer func() { _ = storage.Close(context.Background()) }()
		return use(storage)
	}
	cases := durabletesting.CreateStorageConformance(provider)
	for _, testCase := range cases {
		if err := testCase.Run(context.Background()); err != nil {
			t.Fatalf("%s: %v", testCase.Name, err)
		}
	}
	if calls != len(cases) {
		t.Fatalf("provider calls = %d want %d", calls, len(cases))
	}
}
