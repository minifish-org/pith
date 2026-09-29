// This file is a Go port of packages/ai/src/providers/zai-coding-cn.ts from Pi at
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

// ZaiCodingCnProvider builds the Z.AI Coding CN provider.
func ZaiCodingCnProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderZaiCodingCN,
		Name:    "Z.AI Coding CN",
		BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
		Auth:    envAuth("Z.AI Coding CN API key", "ZAI_CODING_CN_API_KEY"),
		Models:  catalogModelList(catalog.ZAI_CODING_CN_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
