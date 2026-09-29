// This file is a Go port of packages/ai/src/auth/resolve.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// ModelsErrorCode is a stable error discriminator used by Models callers.
type ModelsErrorCode string

// Models error codes.
const (
	ModelsErrorModelSource     ModelsErrorCode = "model_source"
	ModelsErrorModelValidation ModelsErrorCode = "model_validation"
	ModelsErrorProvider        ModelsErrorCode = "provider"
	ModelsErrorStream          ModelsErrorCode = "stream"
	ModelsErrorAuth            ModelsErrorCode = "auth"
	ModelsErrorOAuth           ModelsErrorCode = "oauth"
)

// ModelsError is an auth/model resolution error carrying a stable code and the
// underlying reason in its message.
type ModelsError struct {
	Code    ModelsErrorCode
	Message string
	Cause   error
}

// NewModelsError builds a ModelsError, folding the cause detail into the
// message the way upstream does.
func NewModelsError(code ModelsErrorCode, message string, cause error) *ModelsError {
	return &ModelsError{Code: code, Message: withCauseDetail(message, cause), Cause: cause}
}

// Error implements error.
func (e *ModelsError) Error() string { return e.Message }

// Unwrap exposes the underlying cause.
func (e *ModelsError) Unwrap() error { return e.Cause }

func withCauseDetail(message string, cause error) string {
	if cause == nil {
		return message
	}
	detail := utils.FormatThrownValue(cause)
	if detail == "" || containsDetail(message, detail) {
		return message
	}
	return message + ": " + detail
}

func containsDetail(message, detail string) bool {
	return len(detail) > 0 && len(message) >= len(detail) && indexOf(message, detail) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// AuthResolutionOverrides are the per-call auth overrides.
type AuthResolutionOverrides struct {
	APIKey *string
	Env    types.ProviderEnv
	// MinOAuthValidityMs requires this much remaining OAuth-token validity;
	// defaults to five minutes.
	MinOAuthValidityMs *float64
	Signal             context.Context
}

// AuthProviderSpec is the provider view resolveProviderAuth needs.
type AuthProviderSpec struct {
	ID   string
	Auth authtypes.ProviderAuth
}

// NowMs is the clock used for OAuth expiry checks. It is replaceable in tests.
var NowMs = func() float64 { return float64(time.Now().UnixMilli()) }

// ResolveProviderAuth resolves auth for a provider. A stored credential owns
// the provider: ambient/env is consulted only when nothing is stored. No
// silent env fallback after a failed refresh or for a credential type without
// a matching handler.
//
// Ports `resolveProviderAuth` from packages/ai/src/auth/resolve.ts.
func ResolveProviderAuth(ctx context.Context, provider AuthProviderSpec, credentials authtypes.CredentialStore, authContext authtypes.AuthContext, overrides *AuthResolutionOverrides) (*authtypes.AuthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	signal := ctx
	if overrides != nil && overrides.Signal != nil {
		signal = overrides.Signal
	}
	if signal == ctx {
		return resolveProviderAuthWithSignal(ctx, provider, credentials, authContext, overrides, signal)
	}
	return utils.RaceWithAbortSignal(func() (*authtypes.AuthResult, error) {
		return resolveProviderAuthWithSignal(ctx, provider, credentials, authContext, overrides, signal)
	}, signal)
}

func resolveProviderAuthWithSignal(ctx context.Context, provider AuthProviderSpec, credentials authtypes.CredentialStore, authContext authtypes.AuthContext, overrides *AuthResolutionOverrides, signal context.Context) (*authtypes.AuthResult, error) {
	if err := signalErr(signal); err != nil {
		return nil, err
	}
	requestAuthContext := authContext
	if overrides != nil && len(overrides.Env) > 0 {
		requestAuthContext = overlayEnvAuthContext{authContext, overrides.Env}
	}

	if overrides != nil && overrides.APIKey != nil && provider.Auth.APIKey != nil {
		credential := &authtypes.ApiKeyCredential{
			Type: authtypes.CredentialTypeAPIKey,
			Key:  overrides.APIKey,
			Env:  overrides.Env,
		}
		return resolveAPIKey(ctx, requestAuthContext, provider.Auth.APIKey, provider.ID, credential, signal)
	}

	stored, err := readCredential(ctx, credentials, provider.ID, signal)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		if stored.CredentialType() == authtypes.CredentialTypeOAuth && provider.Auth.OAuth != nil {
			oauth, ok := stored.(*authtypes.OAuthCredential)
			if !ok {
				return nil, nil
			}
			var minValidity *float64
			if overrides != nil {
				minValidity = overrides.MinOAuthValidityMs
			}
			return resolveStoredOAuth(ctx, credentials, provider.ID, provider.Auth.OAuth, oauth, signal, minValidity)
		}
		if stored.CredentialType() == authtypes.CredentialTypeAPIKey && provider.Auth.APIKey != nil {
			apiCredential, ok := stored.(*authtypes.ApiKeyCredential)
			if !ok {
				return nil, nil
			}
			if overrides != nil && len(overrides.Env) > 0 {
				merged := &authtypes.ApiKeyCredential{Type: apiCredential.Type, Key: apiCredential.Key, Env: apiCredential.Env}
				merged.Env = mergeProviderEnv(apiCredential.Env, overrides.Env)
				apiCredential = merged
			}
			return resolveAPIKey(ctx, requestAuthContext, provider.Auth.APIKey, provider.ID, apiCredential, signal)
		}
		return nil, nil
	}

	// Ambient (env vars, AWS profiles, ADC files).
	if provider.Auth.APIKey != nil {
		return resolveAPIKey(ctx, requestAuthContext, provider.Auth.APIKey, provider.ID, nil, signal)
	}
	return nil, nil
}

func mergeProviderEnv(base, overlay types.ProviderEnv) types.ProviderEnv {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := make(types.ProviderEnv, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

type overlayEnvAuthContext struct {
	base authtypes.AuthContext
	env  types.ProviderEnv
}

func (o overlayEnvAuthContext) Env(name string) (*string, error) {
	if value, ok := o.env[name]; ok && value != nil && *value != "" {
		return value, nil
	}
	return o.base.Env(name)
}

func (o overlayEnvAuthContext) FileExists(path string) (bool, error) {
	return o.base.FileExists(path)
}

const (
	defaultOAuthMinimumValidityMs = 5 * 60 * 1000
	defaultOAuthRefreshTimeout    = 15 * time.Second
)

// resolveStoredOAuth implements OAuth resolution with double-checked locking:
// tokens with less than five minutes remaining lock, re-check expiry under the
// lock, refresh once globally, and persist the rotated credential before
// release.
func resolveStoredOAuth(ctx context.Context, credentials authtypes.CredentialStore, providerID string, oauth *authtypes.OAuthAuth, stored *authtypes.OAuthCredential, signal context.Context, minOAuthValidityMs *float64) (*authtypes.AuthResult, error) {
	minimumValidityMs := float64(defaultOAuthMinimumValidityMs)
	if minOAuthValidityMs != nil && *minOAuthValidityMs > minimumValidityMs {
		minimumValidityMs = *minOAuthValidityMs
	}
	expiresSoon := func(credential *authtypes.OAuthCredential) bool {
		return NowMs()+minimumValidityMs >= credential.Expires
	}
	credential := stored

	if expiresSoon(credential) {
		// Optimistic check said expired; the authoritative check runs under the
		// lock.
		post, err := credentials.Modify(ctx, providerID, func(current authtypes.Credential) (authtypes.Credential, error) {
			currentOAuth, ok := current.(*authtypes.OAuthCredential)
			if !ok {
				return nil, nil // logged out meanwhile
			}
			if !expiresSoon(currentOAuth) {
				return nil, nil // another request refreshed
			}
			if oauth.Refresh == nil {
				return nil, NewModelsError(ModelsErrorOAuth, fmt.Sprintf("OAuth refresh failed for %s", providerID), errors.New("oauth flow has no refresh"))
			}
			refreshCtx, cancel := context.WithTimeout(signal, defaultOAuthRefreshTimeout)
			defer cancel()
			refreshed, refreshErr := oauth.Refresh(refreshCtx, currentOAuth)
			if refreshErr != nil {
				return nil, NewModelsError(ModelsErrorOAuth, fmt.Sprintf("OAuth refresh failed for %s", providerID), refreshErr)
			}
			return refreshed, nil
		}, &authtypes.AuthOperationOptions{Signal: signal})
		if err != nil {
			var modelsErr *ModelsError
			if errors.As(err, &modelsErr) {
				return nil, err
			}
			return nil, NewModelsError(ModelsErrorAuth, fmt.Sprintf("Credential store modify failed for %s", providerID), err)
		}
		postOAuth, ok := post.(*authtypes.OAuthCredential)
		if !ok {
			return nil, nil // logged out meanwhile
		}
		credential = postOAuth
		if minOAuthValidityMs != nil && expiresSoon(credential) {
			return nil, NewModelsError(ModelsErrorOAuth, fmt.Sprintf("OAuth refresh returned a token that expires too soon for %s", providerID), nil)
		}
	}

	if oauth.ToAuth == nil {
		return nil, NewModelsError(ModelsErrorOAuth, fmt.Sprintf("OAuth auth derivation failed for %s", providerID), errors.New("oauth flow has no toAuth"))
	}
	auth, err := oauth.ToAuth(ctx, credential)
	if err != nil {
		return nil, NewModelsError(ModelsErrorOAuth, fmt.Sprintf("OAuth auth derivation failed for %s", providerID), err)
	}
	source := "OAuth"
	return &authtypes.AuthResult{Auth: auth, Source: &source}, nil
}

func resolveAPIKey(ctx context.Context, authContext authtypes.AuthContext, apiKey *authtypes.ApiKeyAuth, providerID string, credential *authtypes.ApiKeyCredential, signal context.Context) (*authtypes.AuthResult, error) {
	if apiKey.Resolve == nil {
		return nil, nil
	}
	result, err := apiKey.Resolve(ctx, authtypes.ApiKeyResolveInput{Ctx: authContext, Credential: credential, Signal: signal})
	if err != nil {
		return nil, NewModelsError(ModelsErrorAuth, fmt.Sprintf("API key auth failed for provider %s", providerID), err)
	}
	return result, nil
}

func readCredential(ctx context.Context, credentials authtypes.CredentialStore, providerID string, signal context.Context) (authtypes.Credential, error) {
	stored, err := credentials.Read(ctx, providerID, &authtypes.AuthOperationOptions{Signal: signal})
	if err != nil {
		return nil, NewModelsError(ModelsErrorAuth, fmt.Sprintf("Credential store read failed for %s", providerID), err)
	}
	return stored, nil
}
