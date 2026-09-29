// This file is a Go port of packages/ai/src/providers/anthropic.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"context"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/auth"
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

func anthropicAPIKeyAuth() authtypes.ApiKeyAuth {
	return authtypes.ApiKeyAuth{
		Name: "Anthropic API key",
		Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.ApiKeyCredential, error) {
			if err := interaction.Signal().Err(); err != nil {
				return nil, err
			}
			key, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSecret,
				Message: "Enter Anthropic API key",
			})
			if err != nil {
				return nil, err
			}
			if err := interaction.Signal().Err(); err != nil {
				return nil, err
			}
			return authtypes.NewApiKeyCredential(key), nil
		},
		Resolve: func(_ context.Context, input authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
			if err := signalError(input.Signal); err != nil {
				return nil, err
			}
			if input.Credential != nil && input.Credential.Key != nil {
				source := "stored credential"
				return &authtypes.AuthResult{
					Auth:   authtypes.ModelAuth{APIKey: input.Credential.Key},
					Env:    input.Credential.Env,
					Source: &source,
				}, nil
			}

			authToken, err := input.Ctx.Env(ai.ANTHROPIC_AUTH_TOKEN_ENV)
			if err != nil {
				return nil, err
			}
			if err := signalError(input.Signal); err != nil {
				return nil, err
			}
			if authToken != nil && *authToken != "" {
				source := ai.ANTHROPIC_AUTH_TOKEN_ENV
				bearer := "Bearer " + *authToken
				return &authtypes.AuthResult{
					Auth:   authtypes.ModelAuth{Headers: types.ProviderHeaders{"Authorization": &bearer}},
					Source: &source,
				}, nil
			}

			for _, envVar := range []string{ai.ANTHROPIC_OAUTH_TOKEN_ENV, ai.ANTHROPIC_API_KEY_ENV} {
				apiKey, err := input.Ctx.Env(envVar)
				if err != nil {
					return nil, err
				}
				if err := signalError(input.Signal); err != nil {
					return nil, err
				}
				if apiKey != nil && *apiKey != "" {
					source := envVar
					return &authtypes.AuthResult{Auth: authtypes.ModelAuth{APIKey: apiKey}, Source: &source}, nil
				}
			}
			return nil, nil
		},
	}
}

// AnthropicProvider builds the Anthropic provider.
func AnthropicProvider() ai.Provider {
	apiKey := anthropicAPIKeyAuth()
	anthropicOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:           "Anthropic (Claude Pro/Max)",
		IsSubscription: true,
		Load:           oauth.LoadAnthropicOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderAnthropic,
		Name:    "Anthropic",
		BaseURL: "https://api.anthropic.com",
		Auth: authtypes.ProviderAuth{
			APIKey: &apiKey,
			OAuth:  &anthropicOAuth,
		},
		Models: catalogModelList(catalog.ANTHROPIC_MODELS),
		API:    api.AnthropicMessagesApi(),
	})
}

func signalError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
