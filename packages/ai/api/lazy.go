// This file is a Go port of packages/ai/src/api/lazy.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// lazyStream returns a stream synchronously while running async setup (auth
// resolution, lazy module loading) behind it. The Go port keeps that shape: the
// caller receives a live *AssistantMessageEventStream immediately and the setup
// function runs in a goroutine, forwarding events as they arrive.
package api

import (
	"context"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
)

// createSetupErrorMessage builds the error assistant message that terminates a
// failed lazy setup.
func createSetupErrorMessage(model *types.Model, err error) types.AssistantMessage {
	message := types.AssistantMessage{
		Role:       types.AssistantMessageRole,
		Content:    []types.ContentBlock{},
		StopReason: types.StopReasonError,
		Timestamp:  nowMillis(),
	}
	if model != nil {
		message.Api = model.Api
		message.Provider = model.Provider
		message.Model = model.Id
	}
	if err != nil {
		text := err.Error()
		message.ErrorMessage = &text
	} else {
		text := "Unknown error"
		message.ErrorMessage = &text
	}
	return message
}

// LazyStream returns a stream synchronously while running async setup behind it.
// Setup failures terminate the stream with an error event.
//
// The setup function receives the operation context so a cancelled stream does
// not keep loading provider modules. Setup runs on its own goroutine; its events
// are forwarded to the returned stream in order.
func LazyStream(ctx context.Context, model *types.Model, setup func(context.Context) (*types.AssistantMessageEventStream, error)) *types.AssistantMessageEventStream {
	outer := types.NewAssistantMessageEventStream()
	go func() {
		inner, err := setup(ctx)
		if err != nil {
			terminateWithSetupError(outer, model, err)
			return
		}
		if inner == nil {
			terminateWithSetupError(outer, model, fmt.Errorf("lazy stream setup returned no stream"))
			return
		}
		forwardStream(ctx, outer, inner)
	}()
	return outer
}

// terminateWithSetupError pushes an error event and ends the stream with the
// setup error message.
func terminateWithSetupError(outer *types.AssistantMessageEventStream, model *types.Model, err error) {
	message := createSetupErrorMessage(model, err)
	reason := types.StopReasonError
	outer.Push(types.NewErrorEvent(reason, message))
	outer.End(&message)
}

// forwardStream drains the inner stream, re-pushing every event, then ends the
// outer stream with the inner result. Draining stops when ctx is cancelled so an
// abandoned setup cannot block forever on a producer that already stopped.
func forwardStream(ctx context.Context, target *types.AssistantMessageEventStream, source *types.AssistantMessageEventStream) {
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-source.Next():
			if !ok {
				return
			}
			if item.Done {
				result, err := source.Result(ctx)
				if err != nil {
					// No terminal event was observed; synthesize an error so the
					// outer stream still resolves.
					terminateWithSetupError(target, nil, err)
					return
				}
				target.End(&result)
				return
			}
			target.Push(item.Value)
		}
	}
}

// staticProviderStreams adapts the typed, provider-specific stream functions
// to the uniform types.ProviderStreams contract. The provider functions take
// their concrete option structs, so each lazy API module wraps them by
// projecting the shared StreamOptions into the concrete type. Deferred-response
// methods are unsupported by these modules and report that explicitly.
type staticProviderStreams struct {
	StreamFn       func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream
	StreamSimpleFn func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream
}

// Stream delegates to the configured typed stream function.
func (s staticProviderStreams) Stream(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	return s.StreamFn(model, context, options)
}

// StreamSimple delegates to the configured typed simple-stream function.
func (s staticProviderStreams) StreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return s.StreamSimpleFn(model, context, options)
}

// FetchDeferred reports that the API module does not support deferred responses.
func (s staticProviderStreams) FetchDeferred(_ *types.Model, _ types.DeferredHandle, _ *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	return nil, fmt.Errorf("API does not support deferred responses")
}

// CancelDeferred reports that the API module cannot cancel deferred responses.
func (s staticProviderStreams) CancelDeferred(_ *types.Model, _ types.DeferredHandle, _ *types.DeferredCancelOptions) error {
	return fmt.Errorf("API cannot cancel deferred responses")
}

// LazyApiCapabilities declares which optional deferred-response methods the
// lazily loaded API implementation supports.
type LazyApiCapabilities struct {
	FetchDeferred  bool
	CancelDeferred bool
}

// LazyApi wraps a dynamically loaded API implementation module as a
// types.ProviderStreams. The module loads on the first stream call; load
// failures terminate the returned stream with an error event.
//
// The load function is called once per stream invocation, matching upstream's
// host-module cache; callers that want single-load semantics should memoize
// inside load.
func LazyApi(ctx context.Context, load func(context.Context) (types.ProviderStreams, error), capabilities *LazyApiCapabilities) types.ProviderStreams {
	implementation := &lazyProviderStreams{load: load}
	if capabilities != nil {
		implementation.fetchDeferred = capabilities.FetchDeferred
		implementation.cancelDeferred = capabilities.CancelDeferred
	}
	return implementation
}

type lazyProviderStreams struct {
	load           func(context.Context) (types.ProviderStreams, error)
	fetchDeferred  bool
	cancelDeferred bool
}

func (l *lazyProviderStreams) Stream(model *types.Model, context_ *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	return LazyStream(context.Background(), model, func(ctx context.Context) (*types.AssistantMessageEventStream, error) {
		impl, err := l.load(ctx)
		if err != nil {
			return nil, err
		}
		return impl.Stream(model, context_, options), nil
	})
}

func (l *lazyProviderStreams) StreamSimple(model *types.Model, context_ *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return LazyStream(context.Background(), model, func(ctx context.Context) (*types.AssistantMessageEventStream, error) {
		impl, err := l.load(ctx)
		if err != nil {
			return nil, err
		}
		return impl.StreamSimple(model, context_, options), nil
	})
}

func (l *lazyProviderStreams) FetchDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	if !l.fetchDeferred {
		return nil, fmt.Errorf("API does not support deferred responses")
	}
	impl, err := l.load(context.Background())
	if err != nil {
		return nil, err
	}
	if impl == nil {
		return nil, fmt.Errorf("API does not support deferred responses")
	}
	return impl.FetchDeferred(model, handle, options)
}

func (l *lazyProviderStreams) CancelDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error {
	if !l.cancelDeferred {
		return fmt.Errorf("API cannot cancel deferred responses")
	}
	impl, err := l.load(context.Background())
	if err != nil {
		return err
	}
	if impl == nil {
		return fmt.Errorf("API cannot cancel deferred responses")
	}
	return impl.CancelDeferred(model, handle, options)
}
