// This file is a Go port of packages/ai/src/api/openai-responses.lazy.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"context"

	"github.com/minifish-org/pith/packages/ai/types"
)

// OpenAIResponsesApi returns the lazily loaded OpenAI Responses API module.
//
// Go has no dynamic module import, so the load function returns the statically
// linked implementation; the lazy stream ownership contract is preserved.
func OpenAIResponsesApi() types.ProviderStreams {
	return LazyApi(context.Background(), func(context.Context) (types.ProviderStreams, error) {
		return staticProviderStreams{
			StreamFn: func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
				var typed OpenAIResponsesOptions
				if options != nil {
					typed.StreamOptions = *options
				}
				return OpenAIResponsesStream(model, context, &typed)
			},
			StreamSimpleFn: func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
				return OpenAIResponsesStreamSimple(model, context, options)
			},
		}, nil
	}, nil)
}
