// This file is a Go port of packages/ai/src/providers/xiaomi-token-plan-ams.ts
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

// XiaomiTokenPlanAmsProvider builds the Xiaomi Token Plan AMS provider.
func XiaomiTokenPlanAmsProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderXiaomiTokenPlanAMS,
		Name:    "Xiaomi Token Plan AMS",
		BaseURL: "https://token-plan-ams.xiaomimimo.com/v1",
		Auth:    envAuth("Xiaomi Token Plan AMS API key", "XIAOMI_TOKEN_PLAN_AMS_API_KEY"),
		Models:  catalogModelList(catalog.XIAOMI_TOKEN_PLAN_AMS_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
