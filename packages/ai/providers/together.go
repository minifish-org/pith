// This file is a Go port of packages/ai/src/providers/together.ts from Pi at
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

// TogetherProvider builds the Together provider.
func TogetherProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderTogether,
		Name:    "Together",
		BaseURL: "https://api.together.ai/v1",
		Auth:    envAuth("Together API key", "TOGETHER_API_KEY"),
		Models:  catalogModelList(catalog.TOGETHER_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
