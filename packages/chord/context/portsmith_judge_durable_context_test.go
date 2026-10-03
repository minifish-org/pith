package chordcontext_test

import (
	"context"
	"errors"
	chordcontext "github.com/minifish-org/pith/packages/chord/context"
	"testing"
	"time"
)

func TestPortsmithJudgeDurableContextCleanupAndIdentity(t *testing.T) {
	key := chordcontext.CreateContextKey[string]("same name")
	other := chordcontext.CreateContextKey[string]("same name")
	parent := chordcontext.WithContextValue(key, "worker", chordcontext.BackgroundContext)
	child, cancel := chordcontext.WithCancel(parent)
	cancel()
	select {
	case <-child.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child cancellation not propagated")
	}
	cleanup := chordcontext.WithoutAbortSignal(child)
	if cleanup.Err() != nil || cleanup.Done() != nil {
		t.Fatal("cleanup retained caller cancellation")
	}
	if v, ok := chordcontext.Value(cleanup, key); !ok || v != "worker" {
		t.Fatal("cleanup lost invocation value")
	}
	if _, ok := chordcontext.Value(cleanup, other); ok {
		t.Fatal("keys alias by description")
	}
	if parent.Err() != nil {
		t.Fatal("child cancellation cancelled parent")
	}
	signal, stop := context.WithCancel(context.Background())
	combined := chordcontext.WithAbortSignal(signal, parent)
	stop()
	select {
	case <-combined.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("external abort signal not propagated")
	}
}

func TestPortsmithJudgeDurableContextWaiterCancellationDoesNotCancelWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	settled := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, e := chordcontext.AwaitWithContext(ctx, func() (string, error) { close(started); <-release; close(settled); return "finished", nil })
		result <- e
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("work not started")
	}
	cancel()
	select {
	case e := <-result:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("waiter cancellation: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter ignored cancellation")
	}
	select {
	case <-settled:
		t.Fatal("cancelled waiter cancelled underlying work")
	default:
	}
	close(release)
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("underlying work did not remain runnable")
	}
	v, e := chordcontext.AwaitWithContext(context.Background(), func() (string, error) { return "normal", nil })
	if e != nil || v != "normal" {
		t.Fatalf("normal waiter: %q %v", v, e)
	}
}
