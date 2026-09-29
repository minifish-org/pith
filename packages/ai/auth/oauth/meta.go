// This file is a Go port of packages/ai/src/auth/oauth/meta.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

const (
	metaClientID          = "1031625952748946"
	metaAuthHost          = "https://auth.meta.com"
	metaDeviceAuthURL     = metaAuthHost + "/oidc/device/authorization/"
	metaDeviceTokenURL    = metaAuthHost + "/oidc/device/token/"
	metaAPIKeyMintURL     = "https://api.meta.ai/muse-code/key"
	metaAPIKeyLifetimeMs  = 24 * 60 * 60 * 1000
	metaRequestTimeout    = 30 * time.Second
	metaDevicePollExpires = 0
)

var metaHTTPClient = &http.Client{Timeout: 60 * time.Second}

type metaDeviceAuthorization struct {
	DeviceCode       string
	UserCode         string
	VerificationURI  string
	IntervalSeconds  *float64
	ExpiresInSeconds *float64
}

func metaRequestSignal(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, metaRequestTimeout)
}

func metaReadJSON(response *http.Response) map[string]any {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func metaErrorDetail(jsonBody map[string]any) string {
	for _, key := range []string{"error_description", "detail", "message", "error"} {
		if value, ok := jsonBody[key].(string); ok && strings.TrimSpace(value) != "" {
			return ": " + strings.TrimSpace(value)
		}
	}
	return ""
}

func metaTrustedHTTPURL(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return "", false
	}
	parsed, err := url.Parse(text)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", false
	}
	return parsed.String(), true
}

func metaPositiveNumber(value any) *float64 {
	number, ok := value.(float64)
	if !ok || isInfOrNaN(number) || number <= 0 {
		return nil
	}
	return &number
}

func startMetaDeviceAuthorization(ctx context.Context, signal context.Context) (*metaDeviceAuthorization, error) {
	requestCtx, cancel := metaRequestSignal(ctx)
	defer cancel()
	form := url.Values{}
	form.Set("client_id", metaClientID)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, metaDeviceAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := metaHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	jsonBody := metaReadJSON(response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Meta device authorization failed with status %d%s", response.StatusCode, metaErrorDetail(jsonBody))
	}
	deviceCode, _ := jsonBody["device_code"].(string)
	userCode, _ := jsonBody["user_code"].(string)
	verificationURI, okComplete := metaTrustedHTTPURL(jsonBody["verification_uri_complete"])
	if !okComplete {
		verificationURI, okComplete = metaTrustedHTTPURL(jsonBody["verification_uri"])
	}
	if deviceCode == "" || userCode == "" || !okComplete {
		encoded, _ := json.Marshal(jsonBody)
		return nil, fmt.Errorf("Invalid Meta device authorization response: %s", string(encoded))
	}
	return &metaDeviceAuthorization{
		DeviceCode:       deviceCode,
		UserCode:         userCode,
		VerificationURI:  verificationURI,
		IntervalSeconds:  metaPositiveNumber(jsonBody["interval"]),
		ExpiresInSeconds: metaPositiveNumber(jsonBody["expires_in"]),
	}, nil
}

func pollForMetaIdentityToken(ctx context.Context, device *metaDeviceAuthorization, signal context.Context) (string, error) {
	return PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[string]{
		IntervalSeconds:     device.IntervalSeconds,
		ExpiresInSeconds:    device.ExpiresInSeconds,
		WaitBeforeFirstPoll: true,
		Signal:              signal,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			requestCtx, cancel := metaRequestSignal(ctx)
			defer cancel()
			form := url.Values{}
			form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
			form.Set("device_code", device.DeviceCode)
			form.Set("client_id", metaClientID)
			request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, metaDeviceTokenURL, strings.NewReader(form.Encode()))
			if err != nil {
				return OAuthDeviceCodePollResult[string]{}, err
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept", "application/json")
			response, err := metaHTTPClient.Do(request)
			if err != nil {
				return OAuthDeviceCodePollResult[string]{}, err
			}
			defer response.Body.Close()
			jsonBody := metaReadJSON(response)
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if token, ok := jsonBody["access_token"].(string); ok && token != "" {
					return DeviceCodeComplete(token), nil
				}
			}
			switch jsonBody["error"] {
			case "authorization_pending":
				return DeviceCodePending[string](), nil
			case "slow_down":
				return DeviceCodeSlowDown[string](metaPositiveNumber(jsonBody["interval"])), nil
			case "access_denied":
				return DeviceCodeFailed[string]("Meta login was denied."), nil
			case "expired_token":
				return DeviceCodeFailed[string]("Meta device authorization expired. Please restart login."), nil
			default:
				return DeviceCodeFailed[string](fmt.Sprintf("Meta device token request failed with status %d%s", response.StatusCode, metaErrorDetail(jsonBody))), nil
			}
		},
	})
}

func mintMetaAPIKey(ctx context.Context, identityToken string, signal context.Context) (*authtypes.OAuthCredential, error) {
	requestCtx, cancel := metaRequestSignal(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, metaAPIKeyMintURL, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+identityToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-version", "1.0.0")
	response, err := metaHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	jsonBody := metaReadJSON(response)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Meta session expired (status %d). Run `/login meta` to sign in again.%s", response.StatusCode, metaErrorDetail(jsonBody))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Meta API key mint failed with status %d%s", response.StatusCode, metaErrorDetail(jsonBody))
	}
	apiKey, _ := jsonBody["api_key"].(string)
	if apiKey == "" {
		actionURL, ok := metaTrustedHTTPURL(jsonBody["action_url"])
		if ok {
			return nil, errors.New("Meta did not issue an API key. Complete setup at " + actionURL)
		}
		return nil, errors.New("Meta did not issue an API key.")
	}
	return &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Refresh: identityToken,
			Access:  apiKey,
			Expires: float64(time.Now().UnixMilli()) + metaAPIKeyLifetimeMs,
		},
		Type: authtypes.CredentialTypeOAuth,
	}, nil
}

func loginMeta(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	device, err := startMetaDeviceAuthorization(ctx, interaction.Signal())
	if err != nil {
		if interaction.Signal() != nil && interaction.Signal().Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	interaction.Notify(authtypes.AuthEvent{
		Type:             authtypes.AuthEventDeviceCode,
		UserCode:         &device.UserCode,
		VerificationURI:  &device.VerificationURI,
		IntervalSeconds:  device.IntervalSeconds,
		ExpiresInSeconds: device.ExpiresInSeconds,
	})
	identityToken, err := pollForMetaIdentityToken(ctx, device, interaction.Signal())
	if err != nil {
		if interaction.Signal() != nil && interaction.Signal().Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: stringPtr("Enabling Meta Model API access...")})
	credential, err := mintMetaAPIKey(ctx, identityToken, interaction.Signal())
	if err != nil {
		if interaction.Signal() != nil && interaction.Signal().Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	return credential, nil
}

// MetaOAuth is the Meta (Muse subscription) OAuth flow.
//
// Ports `metaOAuth` from packages/ai/src/auth/oauth/meta.ts.
var MetaOAuth = &authtypes.OAuthAuth{
	Name:           "Meta (Muse subscription)",
	IsSubscription: true,
	LoginLabel:     "Sign in with Meta",
	Login:          loginMeta,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return mintMetaAPIKey(ctx, credential.Refresh, ctx)
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
	},
}
