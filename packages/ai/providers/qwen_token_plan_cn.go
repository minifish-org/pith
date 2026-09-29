// This file is a Go port of packages/ai/src/providers/qwen-token-plan-cn.ts from
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

// QwenTokenPlanCnProvider builds the Qwen Token Plan CN provider.
func QwenTokenPlanCnProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderQwenTokenPlanCN,
		Name:    "Qwen Token Plan CN",
		BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
		Auth:    envAuth("Qwen Token Plan CN API key", "QWEN_TOKEN_PLAN_CN_API_KEY"),
		Models:  catalogModelList(catalog.QWEN_TOKEN_PLAN_CN_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
