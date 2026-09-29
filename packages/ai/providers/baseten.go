// This file is a Go port of packages/ai/src/providers/baseten.ts from Pi at
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

// BasetenProvider builds the Baseten provider.
func BasetenProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderBaseten,
		Name:    "Baseten",
		BaseURL: "https://inference.baseten.co/v1",
		Auth:    envAuth("Baseten API key", "BASETEN_API_KEY"),
		Models:  catalogModelList(catalog.BASETEN_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
