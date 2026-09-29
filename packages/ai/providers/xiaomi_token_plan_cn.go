// This file is a Go port of packages/ai/src/providers/xiaomi-token-plan-cn.ts
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

// XiaomiTokenPlanCnProvider builds the Xiaomi Token Plan CN provider.
func XiaomiTokenPlanCnProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderXiaomiTokenPlanCN,
		Name:    "Xiaomi Token Plan CN",
		BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
		Auth:    envAuth("Xiaomi Token Plan CN API key", "XIAOMI_TOKEN_PLAN_CN_API_KEY"),
		Models:  catalogModelList(catalog.XIAOMI_TOKEN_PLAN_CN_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
