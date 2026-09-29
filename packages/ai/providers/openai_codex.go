// This file is a Go port of packages/ai/src/providers/openai-codex.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/auth"
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// OpenAICodexProvider builds the OpenAI Codex provider.
func OpenAICodexProvider() ai.Provider {
	codexOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:           "OpenAI (ChatGPT Plus/Pro)",
		IsSubscription: true,
		Load:           oauth.LoadOpenAICodexOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderOpenAICodex,
		Name:    "OpenAI Codex",
		BaseURL: "https://chatgpt.com/backend-api",
		Auth:    authtypes.ProviderAuth{OAuth: &codexOAuth},
		Models:  catalogModelList(catalog.OPENAI_CODEX_MODELS),
		API:     api.OpenAICodexResponsesApi(),
	})
}
