package api

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const defaultProviderStreamIdleTimeout = 5 * time.Minute
const defaultProviderStreamHeaderTimeout = 30 * time.Second

type streamIdleKey struct{}

type streamIdleWatch struct {
	mu     sync.Mutex
	timer  *time.Timer
	last   time.Time
	window time.Duration
	idle   time.Duration
	closed bool
	cancel context.CancelCauseFunc
}

// Default requests have no total deadline. An explicit host deadline and the
// caller's abort signal remain authoritative even when bytes keep arriving.
func providerStreamContext(signal <-chan struct{}, timeoutMs, idleMs *int) (context.Context, context.CancelFunc) {
	base, cancelSignal := contextForSignal(context.Background(), signal)
	cancelDeadline := func() {}
	if timeoutMs != nil {
		base, cancelDeadline = context.WithTimeout(base, time.Duration(*timeoutMs)*time.Millisecond)
	}
	idle := defaultProviderStreamIdleTimeout
	if idleMs != nil {
		idle = time.Duration(*idleMs) * time.Millisecond
	}
	ctx, _, stop := newStreamIdleWatch(base, defaultProviderStreamHeaderTimeout, idle)
	return ctx, func() { stop(); cancelDeadline(); cancelSignal() }
}

func newStreamIdleWatch(parent context.Context, headers, idle time.Duration) (context.Context, *streamIdleWatch, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	watch := &streamIdleWatch{last: time.Now(), window: headers, idle: idle, cancel: cancel}
	watch.armLocked()
	return context.WithValue(ctx, streamIdleKey{}, watch), watch, func() { watch.stop(); cancel(context.Canceled) }
}

func (w *streamIdleWatch) armLocked() {
	if w.closed || w.window <= 0 {
		return
	}
	w.timer = time.AfterFunc(w.window, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed || w.window <= 0 {
			return
		}
		remaining := w.window - time.Since(w.last)
		if remaining > 0 {
			w.timer.Reset(remaining)
			return
		}
		w.closed = true
		w.cancel(fmt.Errorf("Provider response idle timeout after %s", w.window))
	})
}

func (w *streamIdleWatch) activity(body bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.last = time.Now()
	if body {
		w.window = w.idle
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	if w.window > 0 {
		if w.timer == nil {
			w.armLocked()
		} else {
			w.timer.Reset(w.window)
		}
	}
}

func (w *streamIdleWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
	}
}

type streamIdleBody struct {
	io.ReadCloser
	ctx       context.Context
	watch     *streamIdleWatch
	stopClose func() bool
}

func watchProviderStreamBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	w, _ := ctx.Value(streamIdleKey{}).(*streamIdleWatch)
	if w == nil || body == nil {
		return body
	}
	w.activity(true)
	return &streamIdleBody{ReadCloser: body, ctx: ctx, watch: w, stopClose: context.AfterFunc(ctx, func() { _ = body.Close() })}
}

func (b *streamIdleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.watch.activity(true)
	}
	if err != nil && b.ctx.Err() != nil {
		err = context.Cause(b.ctx)
	}
	return n, err
}

func (b *streamIdleBody) Close() error {
	b.stopClose()
	b.watch.stop()
	return b.ReadCloser.Close()
}
