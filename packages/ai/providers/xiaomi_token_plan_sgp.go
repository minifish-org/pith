// This file is a Go port of packages/ai/src/providers/xiaomi-token-plan-sgp.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
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

// XiaomiTokenPlanSgpProvider builds the Xiaomi Token Plan SGP provider.
func XiaomiTokenPlanSgpProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderXiaomiTokenPlanSGP,
		Name:    "Xiaomi Token Plan SGP",
		BaseURL: "https://token-plan-sgp.xiaomimimo.com/v1",
		Auth:    envAuth("Xiaomi Token Plan SGP API key", "XIAOMI_TOKEN_PLAN_SGP_API_KEY"),
		Models:  catalogModelList(catalog.XIAOMI_TOKEN_PLAN_SGP_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
