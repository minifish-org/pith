// This file is a Go port of packages/ai/src/providers/ant-ling.ts from Pi at
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

// AntLingProvider builds the Ant Ling provider.
func AntLingProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderAntLing,
		Name:    "Ant Ling",
		BaseURL: "https://api.ant-ling.com/v1",
		Auth:    envAuth("Ant Ling API key", "ANT_LING_API_KEY"),
		Models:  catalogModelList(catalog.ANT_LING_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
