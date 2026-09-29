// This file is a Go port of packages/ai/src/providers/vercel-ai-gateway.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// VercelAIGatewayProvider builds the Vercel AI Gateway provider.
func VercelAIGatewayProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderVercelAIGateway,
		Name:    "Vercel AI Gateway",
		BaseURL: "https://ai-gateway.vercel.sh",
		Auth:    envAuth("Vercel AI Gateway API key", "AI_GATEWAY_API_KEY"),
		Models:  catalogModelList(catalog.VERCEL_AI_GATEWAY_MODELS),
		API:     api.AnthropicMessagesApi(),
	})
}
