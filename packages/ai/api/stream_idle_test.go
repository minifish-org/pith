package api

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestProviderStreamingDefaultHasNoTotalDeadline(t *testing.T) {
	for name, create := range map[string]func() (context.Context, context.CancelFunc){
		"mistral": func() (context.Context, context.CancelFunc) { return mistralRequestContext(nil) },
		"pi":      func() (context.Context, context.CancelFunc) { return piMessagesRequestContext(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := create()
			defer cancel()
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("default stream still has a total deadline")
			}
			watch := ctx.Value(streamIdleKey{}).(*streamIdleWatch)
			if watch.idle != 5*time.Minute || watch.window != 30*time.Second {
				t.Fatal("wrong stream activity windows")
			}
		})
	}
}

func TestStreamIdleActivityRenewsAndIdleClosesBlockedBody(t *testing.T) {
	ctx, watch, cancel := newStreamIdleWatch(context.Background(), time.Second, time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	body := watchProviderStreamBody(ctx, reader)
	defer body.Close()
	got := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, body); got <- err }()
	for i := 0; i < 3; i++ {
		if _, err := writer.Write([]byte("data\n")); err != nil {
			t.Fatal(err)
		}
	}
	watch.mu.Lock()
	last := watch.last
	watch.mu.Unlock()
	if time.Since(last) > 500*time.Millisecond || ctx.Err() != nil {
		t.Fatal("activity was not retained")
	}
	// Exercise actual timer cancellation and blocked-read cleanup without a
	// five-minute test. No implementation-global timeouts are changed.
	watch.mu.Lock()
	watch.last = time.Now().Add(-2 * time.Second)
	watch.timer.Reset(time.Millisecond)
	watch.mu.Unlock()
	select {
	case err := <-got:
		if err == nil || !strings.Contains(err.Error(), "idle timeout") {
			t.Fatalf("idle error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle did not interrupt a blocked read")
	}
}

func TestProviderExplicitTotalDeadlineAndAbortStillWin(t *testing.T) {
	ms := 10
	ctx, cancel := providerStreamContext(nil, &ms, nil)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit deadline ignored")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("explicit deadline = %v", ctx.Err())
	}
	signal := make(chan struct{})
	abortCtx, abort := providerStreamContext(signal, nil, nil)
	defer abort()
	close(signal)
	select {
	case <-abortCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("abort ignored")
	}
}
