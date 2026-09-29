// This file is a Go port of packages/ai/src/api/azure-openai-responses.lazy.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"context"

	"github.com/minifish-org/pith/packages/ai/types"
)

// AzureOpenAIResponsesApi returns the lazily loaded Azure OpenAI Responses API
// module.
func AzureOpenAIResponsesApi() types.ProviderStreams {
	return LazyApi(context.Background(), func(context.Context) (types.ProviderStreams, error) {
		return staticProviderStreams{
			StreamFn: func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
				var typed AzureOpenAIResponsesOptions
				if options != nil {
					typed.StreamOptions = *options
				}
				return AzureOpenAIResponsesStream(model, context, &typed)
			},
			StreamSimpleFn: func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
				return AzureOpenAIResponsesStreamSimple(model, context, options)
			},
		}, nil
	}, nil)
}
