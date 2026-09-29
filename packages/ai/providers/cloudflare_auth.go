// This file is a Go port of packages/ai/src/providers/cloudflare-auth.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"context"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
)

const (
	cloudflareAPIKeyEnv    = "CLOUDFLARE_API_KEY"
	cloudflareAccountIDEnv = "CLOUDFLARE_ACCOUNT_ID"
	cloudflareGatewayIDEnv = "CLOUDFLARE_GATEWAY_ID"
)

type cloudflareAuthKind string

const (
	cloudflareAuthWorkersAI cloudflareAuthKind = "workers-ai"
	cloudflareAuthAIGateway cloudflareAuthKind = "ai-gateway"
)

// resolveCloudflareValue performs the upstream per-field merge: prefer the
// credential value, fall back to the ambient AuthContext.
func resolveCloudflareValue(name string, ctx authtypes.AuthContext, credential *authtypes.ApiKeyCredential, signal context.Context) (*string, error) {
	var fromCredential *string
	if credential != nil {
		if name == cloudflareAPIKeyEnv {
			fromCredential = credential.Key
		} else if credential.Env != nil {
			fromCredential = credential.Env[name]
		}
	}
	if fromCredential != nil {
		return fromCredential, nil
	}
	if err := signalError(signal); err != nil {
		return nil, err
	}
	value, err := ctx.Env(name)
	if err != nil {
		return nil, err
	}
	if err := signalError(signal); err != nil {
		return nil, err
	}
	return value, nil
}

type resolvedCloudflareEnv struct {
	apiKey string
	env    types.ProviderEnv
	source string
}

func resolveCloudflareEnv(kind cloudflareAuthKind, ctx authtypes.AuthContext, credential *authtypes.ApiKeyCredential, signal context.Context) (*resolvedCloudflareEnv, error) {
	apiKey, err := resolveCloudflareValue(cloudflareAPIKeyEnv, ctx, credential, signal)
	if err != nil {
		return nil, err
	}
	accountID, err := resolveCloudflareValue(cloudflareAccountIDEnv, ctx, credential, signal)
	if err != nil {
		return nil, err
	}
	var gatewayID *string
	if kind == cloudflareAuthAIGateway {
		gatewayID, err = resolveCloudflareValue(cloudflareGatewayIDEnv, ctx, credential, signal)
		if err != nil {
			return nil, err
		}
	}

	if apiKey == nil || *apiKey == "" || accountID == nil || *accountID == "" {
		return nil, nil
	}
	if kind == cloudflareAuthAIGateway && (gatewayID == nil || *gatewayID == "") {
		return nil, nil
	}

	env := types.ProviderEnv{cloudflareAccountIDEnv: accountID}
	if gatewayID != nil && *gatewayID != "" {
		env[cloudflareGatewayIDEnv] = gatewayID
	}
	source := cloudflareAPIKeyEnv
	if credential != nil {
		source = "stored credential"
	}
	return &resolvedCloudflareEnv{apiKey: *apiKey, env: env, source: source}, nil
}

// CloudflareWorkersAIAuth is the Cloudflare Workers AI api-key auth.
//
// Ports `cloudflareWorkersAIAuth` from
// packages/ai/src/providers/cloudflare-auth.ts.
func CloudflareWorkersAIAuth() authtypes.ApiKeyAuth {
	return authtypes.ApiKeyAuth{
		Name: "Cloudflare API key",
		Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.ApiKeyCredential, error) {
			key, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSecret,
				Message: "Enter Cloudflare API key",
			})
			if err != nil {
				return nil, err
			}
			accountID, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptText,
				Message: "Enter Cloudflare account ID",
			})
			if err != nil {
				return nil, err
			}
			keyCopy := key
			return &authtypes.ApiKeyCredential{
				Type: authtypes.CredentialTypeAPIKey,
				Key:  &keyCopy,
				Env:  types.ProviderEnv{cloudflareAccountIDEnv: &accountID},
			}, nil
		},
		Resolve: func(_ context.Context, input authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
			resolved, err := resolveCloudflareEnv(cloudflareAuthWorkersAI, input.Ctx, input.Credential, input.Signal)
			if err != nil {
				return nil, err
			}
			if resolved == nil {
				return nil, nil
			}
			apiKey := resolved.apiKey
			source := resolved.source
			return &authtypes.AuthResult{
				Auth:   authtypes.ModelAuth{APIKey: &apiKey},
				Env:    resolved.env,
				Source: &source,
			}, nil
		},
	}
}

// CloudflareAIGatewayAuth is the Cloudflare AI Gateway api-key auth.
//
// Ports `cloudflareAIGatewayAuth` from
// packages/ai/src/providers/cloudflare-auth.ts.
func CloudflareAIGatewayAuth() authtypes.ApiKeyAuth {
	return authtypes.ApiKeyAuth{
		Name: "Cloudflare API key",
		Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.ApiKeyCredential, error) {
			key, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSecret,
				Message: "Enter Cloudflare API key",
			})
			if err != nil {
				return nil, err
			}
			accountID, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptText,
				Message: "Enter Cloudflare account ID",
			})
			if err != nil {
				return nil, err
			}
			gatewayID, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptText,
				Message: "Enter Cloudflare AI Gateway ID",
			})
			if err != nil {
				return nil, err
			}
			keyCopy := key
			return &authtypes.ApiKeyCredential{
				Type: authtypes.CredentialTypeAPIKey,
				Key:  &keyCopy,
				Env: types.ProviderEnv{
					cloudflareAccountIDEnv: &accountID,
					cloudflareGatewayIDEnv: &gatewayID,
				},
			}, nil
		},
		Resolve: func(_ context.Context, input authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
			resolved, err := resolveCloudflareEnv(cloudflareAuthAIGateway, input.Ctx, input.Credential, input.Signal)
			if err != nil {
				return nil, err
			}
			if resolved == nil {
				return nil, nil
			}
			source := resolved.source
			bearer := "Bearer " + resolved.apiKey
			var suppress *string
			return &authtypes.AuthResult{
				Auth: authtypes.ModelAuth{Headers: types.ProviderHeaders{
					"cf-aig-authorization": &bearer,
					"Authorization":        suppress,
					"x-api-key":            suppress,
				}},
				Env:    resolved.env,
				Source: &source,
			}, nil
		},
	}
}
