// This file is a Go port of packages/ai/src/auth/helpers.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package auth

import (
	"context"
	"fmt"
	"sync"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// EnvApiKeyAuth is the standard api-key auth: a stored credential key wins,
// otherwise the first set env var resolves. It includes a Login that prompts
// for the key.
//
// Ports `envApiKeyAuth` from packages/ai/src/auth/helpers.ts.
func EnvApiKeyAuth(name string, envVars []string) authtypes.ApiKeyAuth {
	variables := append([]string(nil), envVars...)
	return authtypes.ApiKeyAuth{
		Name: name,
		Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.ApiKeyCredential, error) {
			if err := signalErr(interaction.Signal()); err != nil {
				return nil, err
			}
			key, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSecret,
				Message: fmt.Sprintf("Enter %s", name),
			})
			if err != nil {
				return nil, err
			}
			if err := signalErr(interaction.Signal()); err != nil {
				return nil, err
			}
			keyCopy := key
			return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &keyCopy}, nil
		},
		Resolve: func(ctx context.Context, input authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
			if err := signalErr(input.Signal); err != nil {
				return nil, err
			}
			if input.Credential != nil && input.Credential.Key != nil && *input.Credential.Key != "" {
				source := "stored credential"
				return &authtypes.AuthResult{
					Auth:   authtypes.ModelAuth{APIKey: input.Credential.Key},
					Env:    input.Credential.Env,
					Source: &source,
				}, nil
			}
			for _, envVar := range variables {
				value, err := input.Ctx.Env(envVar)
				if err != nil {
					return nil, err
				}
				if err := signalErr(input.Signal); err != nil {
					return nil, err
				}
				if value != nil && *value != "" {
					source := envVar
					return &authtypes.AuthResult{
						Auth:   authtypes.ModelAuth{APIKey: value},
						Source: &source,
					}, nil
				}
			}
			return nil, nil
		},
	}
}

// LazyOAuthInput configures LazyOAuth.
type LazyOAuthInput struct {
	Name           string
	IsSubscription bool
	LoginLabel     string
	Load           func() (*authtypes.OAuthAuth, error)
}

// LazyOAuth wraps a dynamically loaded OAuthAuth so provider definitions can
// advertise OAuth without importing the implementation. The flow loads on the
// first login/refresh/toAuth call. The load function runs at most once.
//
// Ports `lazyOAuth` from packages/ai/src/auth/helpers.ts.
func LazyOAuth(input LazyOAuthInput) authtypes.OAuthAuth {
	var once sync.Once
	var loaded *authtypes.OAuthAuth
	var loadErr error
	load := func() (*authtypes.OAuthAuth, error) {
		once.Do(func() {
			loaded, loadErr = input.Load()
		})
		return loaded, loadErr
	}
	return authtypes.OAuthAuth{
		Name:           input.Name,
		IsSubscription: input.IsSubscription,
		LoginLabel:     input.LoginLabel,
		Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
			flow, err := load()
			if err != nil {
				return nil, err
			}
			if flow.Login == nil {
				return nil, fmt.Errorf("auth: oauth flow %q has no login", input.Name)
			}
			return flow.Login(ctx, interaction)
		},
		LoginWithOptions: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction, options *authtypes.LoginOptions) (*authtypes.OAuthCredential, error) {
			flow, err := load()
			if err != nil {
				return nil, err
			}
			if flow.LoginWithOptions != nil {
				return flow.LoginWithOptions(ctx, interaction, options)
			}
			if flow.Login == nil {
				return nil, fmt.Errorf("auth: oauth flow %q has no login", input.Name)
			}
			return flow.Login(ctx, interaction)
		},
		Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
			flow, err := load()
			if err != nil {
				return nil, err
			}
			if flow.Refresh == nil {
				return nil, fmt.Errorf("auth: oauth flow %q has no refresh", input.Name)
			}
			return flow.Refresh(ctx, credential)
		},
		ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
			flow, err := load()
			if err != nil {
				return authtypes.ModelAuth{}, err
			}
			if flow.ToAuth == nil {
				return authtypes.ModelAuth{}, fmt.Errorf("auth: oauth flow %q has no toAuth", input.Name)
			}
			return flow.ToAuth(ctx, credential)
		},
	}
}

func signalErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
