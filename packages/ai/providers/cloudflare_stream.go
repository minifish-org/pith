// This file is a Go port of packages/ai/src/providers/cloudflare-stream.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

func replaceCloudflarePlaceholder(baseURL, name string, env types.ProviderEnv) string {
	if env == nil {
		return baseURL
	}
	value, ok := env[name]
	if !ok || value == nil {
		return baseURL
	}
	return strings.ReplaceAll(baseURL, "{"+name+"}", *value)
}

// ResolveCloudflareModel materializes Cloudflare account/gateway endpoint
// placeholders from the resolved provider env.
//
// Ports `resolveCloudflareModel` from
// packages/ai/src/providers/cloudflare-stream.ts.
func ResolveCloudflareModel(model types.Model, env types.ProviderEnv) types.Model {
	if env == nil {
		return model
	}
	baseURL := replaceCloudflarePlaceholder(model.BaseUrl, cloudflareAccountIDEnv, env)
	baseURL = replaceCloudflarePlaceholder(baseURL, cloudflareGatewayIDEnv, env)
	if baseURL == model.BaseUrl {
		return model
	}
	model.BaseUrl = baseURL
	return model
}

type cloudflareProviderStreams struct {
	inner types.ProviderStreams
}

func (s *cloudflareProviderStreams) Stream(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	var env types.ProviderEnv
	if options != nil {
		env = options.Env
	}
	resolved := ResolveCloudflareModel(*model, env)
	return s.inner.Stream(&resolved, context, options)
}

func (s *cloudflareProviderStreams) StreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	var env types.ProviderEnv
	if options != nil {
		env = options.Env
	}
	resolved := ResolveCloudflareModel(*model, env)
	return s.inner.StreamSimple(&resolved, context, options)
}

func (s *cloudflareProviderStreams) FetchDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	return s.inner.FetchDeferred(model, handle, options)
}

func (s *cloudflareProviderStreams) CancelDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error {
	return s.inner.CancelDeferred(model, handle, options)
}

func (s *cloudflareProviderStreams) DeferredCapabilities() (bool, bool) {
	if capability, ok := s.inner.(interface {
		DeferredCapabilities() (bool, bool)
	}); ok {
		return capability.DeferredCapabilities()
	}
	return false, false
}

// CloudflareStreams wraps an API implementation so Cloudflare account/gateway
// endpoint placeholders materialize from the resolved provider env before
// dispatch.
//
// Ports `cloudflareStreams` from
// packages/ai/src/providers/cloudflare-stream.ts.
func CloudflareStreams(streams types.ProviderStreams) types.ProviderStreams {
	return &cloudflareProviderStreams{inner: streams}
}
