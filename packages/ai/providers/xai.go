// This file is a Go port of packages/ai/src/providers/xai.ts from Pi at
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

// XaiProvider builds the xAI provider.
func XaiProvider() ai.Provider {
	apiKey := auth.EnvApiKeyAuth("xAI API key", []string{"XAI_API_KEY"})
	xaiOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:           "xAI (Grok/X subscription)",
		IsSubscription: true,
		LoginLabel:     "Sign in with SuperGrok or X Premium",
		Load:           oauth.LoadXaiOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderXAI,
		Name:    "xAI",
		BaseURL: "https://api.x.ai/v1",
		Auth:    authtypes.ProviderAuth{APIKey: &apiKey, OAuth: &xaiOAuth},
		Models:  catalogModelList(catalog.XAI_MODELS),
		API:     api.OpenAIResponsesApi(),
	})
}
