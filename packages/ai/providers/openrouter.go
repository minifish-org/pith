// This file is a Go port of packages/ai/src/providers/openrouter.ts from Pi at
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

// OpenrouterProvider builds the OpenRouter provider.
func OpenrouterProvider() ai.Provider {
	apiKey := auth.EnvApiKeyAuth("OpenRouter API key", []string{"OPENROUTER_API_KEY"})
	openrouterOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:       "OpenRouter OAuth",
		LoginLabel: "Sign in with OpenRouter",
		Load:       oauth.LoadOpenRouterOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderOpenRouter,
		Name:    "OpenRouter",
		BaseURL: "https://openrouter.ai/api/v1",
		Auth:    authtypes.ProviderAuth{APIKey: &apiKey, OAuth: &openrouterOAuth},
		Models:  catalogModelList(catalog.OPENROUTER_MODELS),
		APIs: map[types.Api]types.ProviderStreams{
			types.ApiAnthropicMessages: api.AnthropicMessagesApi(),
			types.ApiOpenAICompletions: api.OpenAICompletionsApi(),
		},
	})
}
