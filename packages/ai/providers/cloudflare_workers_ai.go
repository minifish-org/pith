// This file is a Go port of packages/ai/src/providers/cloudflare-workers-ai.ts
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

// CloudflareWorkersAIProvider builds the Cloudflare Workers AI provider.
func CloudflareWorkersAIProvider() ai.Provider {
	authValue := CloudflareWorkersAIAuth()
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:     types.ProviderCloudflareWorkersAI,
		Name:   "Cloudflare Workers AI",
		Auth:   authtypes.ProviderAuth{APIKey: &authValue},
		Models: catalogModelList(catalog.CLOUDFLARE_WORKERS_AI_MODELS),
		API:    CloudflareStreams(api.OpenAICompletionsApi()),
	})
}
