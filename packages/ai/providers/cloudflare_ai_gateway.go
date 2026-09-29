// This file is a Go port of packages/ai/src/providers/cloudflare-ai-gateway.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// CloudflareAIGatewayProvider builds the Cloudflare AI Gateway provider.
//
// The api map is pinned to all three APIs: models.dev's gateway catalog drops
// and restores `workers-ai/*` (openai-completions) entries over time, so
// inference from the model list alone would reject an openai-completions entry
// whenever the generated catalog happens to contain none.
func CloudflareAIGatewayProvider() ai.Provider {
	authValue := CloudflareAIGatewayAuth()
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:     types.ProviderCloudflareAIGateway,
		Name:   "Cloudflare AI Gateway",
		Auth:   authtypes.ProviderAuth{APIKey: &authValue},
		Models: catalogModelList(catalog.CLOUDFLARE_AI_GATEWAY_MODELS),
		APIs: map[types.Api]types.ProviderStreams{
			types.ApiAnthropicMessages: CloudflareStreams(api.AnthropicMessagesApi()),
			types.ApiOpenAICompletions: CloudflareStreams(api.OpenAICompletionsApi()),
			types.ApiOpenAIResponses:   CloudflareStreams(api.OpenAIResponsesApi()),
		},
	})
}
