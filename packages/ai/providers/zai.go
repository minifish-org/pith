// This file is a Go port of packages/ai/src/providers/zai.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
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

// ZaiProvider builds the Z.AI provider.
func ZaiProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderZai,
		Name:    "Z.AI",
		BaseURL: "https://api.z.ai/api/coding/paas/v4",
		Auth:    envAuth("Z.AI API key", "ZAI_API_KEY"),
		Models:  catalogModelList(catalog.ZAI_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
