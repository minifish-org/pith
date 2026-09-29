// This file is a Go port of packages/ai/src/api/mistral-conversations.lazy.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"context"

	"github.com/minifish-org/pith/packages/ai/types"
)

// MistralConversationsApi returns the lazily loaded Mistral Conversations API
// module. It mirrors the upstream `mistralConversationsApi()` factory: the
// module loads on first stream use and a load failure terminates the returned
// stream.
func MistralConversationsApi() types.ProviderStreams {
	return LazyApi(context.Background(), func(context.Context) (types.ProviderStreams, error) {
		return staticProviderStreams{
			StreamFn: func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
				var typed MistralOptions
				if options != nil {
					typed.StreamOptions = *options
				}
				return MistralConversationsStream(model, context, &typed)
			},
			StreamSimpleFn: func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
				return MistralConversationsStreamSimple(model, context, options)
			},
		}, nil
	}, nil)
}
