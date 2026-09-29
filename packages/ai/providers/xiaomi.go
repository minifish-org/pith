// This file is a Go port of packages/ai/src/providers/xiaomi.ts from Pi at
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

// XiaomiProvider builds the Xiaomi provider.
func XiaomiProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderXiaomi,
		Name:    "Xiaomi",
		BaseURL: "https://api.xiaomimimo.com/v1",
		Auth:    envAuth("Xiaomi API key", "XIAOMI_API_KEY"),
		Models:  catalogModelList(catalog.XIAOMI_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
