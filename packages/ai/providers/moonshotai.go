// This file is a Go port of packages/ai/src/providers/moonshotai.ts from Pi at
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

// MoonshotaiProvider builds the Moonshot AI provider.
func MoonshotaiProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderMoonshotAI,
		Name:    "Moonshot AI",
		BaseURL: "https://api.moonshot.ai/v1",
		Auth:    envAuth("Moonshot AI API key", "MOONSHOT_API_KEY"),
		Models:  catalogModelList(catalog.MOONSHOTAI_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
