package chordcontext

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestContextKeysAreDistinctEvenWithSameDescription(t *testing.T) {
	first := CreateContextKey[string]("example")
	second := CreateContextKey[string]("example")
	ctx := WithContextValue(first, "one", BackgroundContext)
	if value, ok := Value(ctx, first); !ok || value != "one" {
		t.Fatalf("first key lookup = %q/%v", value, ok)
	}
	if value, ok := Value(ctx, second); ok || value != "" {
		t.Fatalf("second key should be absent, got %q/%v", value, ok)
	}
}

func TestWithContextValueKeepsParentIntact(t *testing.T) {
	key := CreateContextKey[int]("count")
	parent := WithContextValue(key, 1, BackgroundContext)
	child := WithContextValue(key, 2, parent)
	if value, _ := Value(parent, key); value != 1 {
		t.Fatalf("parent value = %d", value)
	}
	if value, _ := Value(child, key); value != 2 {
		t.Fatalf("child value = %d", value)
	}
}

func TestWithCancelCancelsChild(t *testing.T) {
	ctx, cancel := WithCancel(BackgroundContext)
	if err := ctx.Err(); err != nil {
		t.Fatalf("unexpected pre-cancel error: %v", err)
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("child did not cancel")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("err = %v", ctx.Err())
	}
}

func TestWithoutAbortSignalKeepsValuesButIgnoresCancellation(t *testing.T) {
	key := CreateContextKey[string]("value")
	parent, cancel := WithCancel(WithContextValue(key, "kept", BackgroundContext))
	cancel()
	detached := WithoutAbortSignal(parent)
	select {
	case <-detached.Done():
		t.Fatal("detached context should not observe parent cancellation")
	default:
	}
	if value, ok := Value(detached, key); !ok || value != "kept" {
		t.Fatalf("detached value = %q/%v", value, ok)
	}
}

func TestAwaitWithContext(t *testing.T) {
	ctx, cancel := WithCancel(BackgroundContext)
	result, err := AwaitWithContext(ctx, func() (string, error) { return "done", nil })
	if err != nil || result != "done" {
		t.Fatalf("await = %q/%v", result, err)
	}
	cancel()
	_, err = AwaitWithContext(ctx, func() (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "late", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("await after cancel = %v", err)
	}
}

func TestWithAbortSignalCombinesCancellation(t *testing.T) {
	parent, parentCancel := WithCancel(BackgroundContext)
	signal, signalCancel := WithCancel(context.Background())
	combined := WithAbortSignal(signal, parent)
	signalCancel()
	select {
	case <-combined.Done():
	case <-time.After(time.Second):
		t.Fatal("combined context did not observe signal cancellation")
	}
	parentCancel()
}
