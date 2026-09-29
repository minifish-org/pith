// This file is a Go port of packages/ai/src/providers/meta.ts from Pi at
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

// MetaProvider builds the Meta provider.
func MetaProvider() ai.Provider {
	apiKey := auth.EnvApiKeyAuth("Meta Model API key", []string{"META_API_KEY"})
	metaOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:           "Meta (Muse subscription)",
		IsSubscription: true,
		LoginLabel:     "Sign in with Meta",
		Load:           oauth.LoadMetaOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderMeta,
		Name:    "Meta",
		BaseURL: "https://api.meta.ai/v1",
		Auth:    authtypes.ProviderAuth{APIKey: &apiKey, OAuth: &metaOAuth},
		Models:  catalogModelList(catalog.META_MODELS),
		API:     api.OpenAIResponsesApi(),
	})
}
