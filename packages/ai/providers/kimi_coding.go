// This file is a Go port of packages/ai/src/providers/kimi-coding.ts from Pi at
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

// KimiCodingProvider builds the Kimi For Coding provider.
func KimiCodingProvider() ai.Provider {
	apiKey := auth.EnvApiKeyAuth("Kimi API key", []string{"KIMI_API_KEY"})
	kimiOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:           "Kimi Code (subscription)",
		IsSubscription: true,
		LoginLabel:     "Sign in with Kimi Code",
		Load:           oauth.LoadKimiCodingOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderKimiCoding,
		Name:    "Kimi For Coding",
		BaseURL: "https://api.kimi.com/coding",
		Auth:    authtypes.ProviderAuth{APIKey: &apiKey, OAuth: &kimiOAuth},
		Models:  catalogModelList(catalog.KIMI_CODING_MODELS),
		API:     api.AnthropicMessagesApi(),
	})
}
