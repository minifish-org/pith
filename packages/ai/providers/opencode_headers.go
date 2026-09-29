// This file is a Go port of packages/ai/src/providers/opencode-headers.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"github.com/minifish-org/pith/packages/ai/types"
)

const openCodeSessionHeader = "x-opencode-session"

func hasProviderHeader(headers types.ProviderHeaders, name string) bool {
	for key := range headers {
		if equalFoldASCII(key, name) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func withOpenCodeSessionHeader(options *types.StreamOptions) *types.StreamOptions {
	if options == nil || options.SessionId == nil || *options.SessionId == "" {
		return options
	}
	if hasProviderHeader(options.Headers, openCodeSessionHeader) {
		return options
	}
	clone := *options
	clone.Headers = make(types.ProviderHeaders, len(options.Headers)+1)
	for key, value := range options.Headers {
		clone.Headers[key] = value
	}
	sessionID := *options.SessionId
	clone.Headers[openCodeSessionHeader] = &sessionID
	return &clone
}

func withOpenCodeSessionHeaderSimple(options *types.SimpleStreamOptions) *types.SimpleStreamOptions {
	if options == nil {
		return nil
	}
	updated := withOpenCodeSessionHeader(&options.StreamOptions)
	if updated == &options.StreamOptions {
		return options
	}
	clone := *options
	clone.StreamOptions = *updated
	return &clone
}

// openCodeHeaderStreams wraps a ProviderStreams adding OpenCode's
// per-conversation routing header before dispatch.
type openCodeHeaderStreams struct {
	inner types.ProviderStreams
}

func (s *openCodeHeaderStreams) Stream(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	return s.inner.Stream(model, context, withOpenCodeSessionHeader(options))
}

func (s *openCodeHeaderStreams) StreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return s.inner.StreamSimple(model, context, withOpenCodeSessionHeaderSimple(options))
}

func (s *openCodeHeaderStreams) FetchDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	return s.inner.FetchDeferred(model, handle, options)
}

func (s *openCodeHeaderStreams) CancelDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error {
	return s.inner.CancelDeferred(model, handle, options)
}

func (s *openCodeHeaderStreams) DeferredCapabilities() (bool, bool) {
	if capability, ok := s.inner.(interface {
		DeferredCapabilities() (bool, bool)
	}); ok {
		return capability.DeferredCapabilities()
	}
	return false, false
}

// WithOpenCodeSessionHeader adds OpenCode's required per-conversation routing
// header before API dispatch.
//
// Ports `withOpenCodeSessionHeader` from
// packages/ai/src/providers/opencode-headers.ts.
func WithOpenCodeSessionHeader(streams types.ProviderStreams) types.ProviderStreams {
	return &openCodeHeaderStreams{inner: streams}
}
