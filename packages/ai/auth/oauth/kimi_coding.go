// This file is a Go port of packages/ai/src/auth/oauth/kimi-coding.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
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
	"github.com/minifish-org/pith/packages/ai/utils"
)

const (
	kimiClientID               = "17e5f671-d194-4dfb-9706-5516cb48c098"
	kimiDefaultOAuthHost       = "https://auth.kimi.com"
	kimiDeviceCodeTimeoutSec   = 15 * 60
	kimiDefaultPollIntervalSec = 5
	kimiRequestTimeout         = 30 * time.Second
	kimiRefreshMaxRetries      = 3
)

var kimiHTTPClient = &http.Client{Timeout: 60 * time.Second}

func kimiOAuthHost() string {
	override := ""
	if value := utils.GetProviderEnvValue("KIMI_CODE_OAUTH_HOST", nil); value != nil && *value != "" {
		override = *value
	} else if value := utils.GetProviderEnvValue("KIMI_OAUTH_HOST", nil); value != nil && *value != "" {
		override = *value
	}
	if override == "" {
		override = kimiDefaultOAuthHost
	}
	return strings.TrimRight(override, "/")
}

func kimiRequestSignal(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, kimiRequestTimeout)
}

func formURLEncode(fields map[string]string) string {
	values := url.Values{}
	for key, value := range fields {
		values.Set(key, value)
	}
	return values.Encode()
}

func kimiReadJSON(response *http.Response) map[string]any {
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

func kimiTrustedHTTPURL(value any) (string, bool) {
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

type kimiDeviceAuthorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	IntervalSeconds         float64
	ExpiresInSeconds        float64
}

type kimiTokenResponse struct {
	Access  string
	Refresh string
	Expires float64
}

func startKimiDeviceAuthorization(ctx context.Context, oauthHost string, signal context.Context) (*kimiDeviceAuthorization, error) {
	requestCtx, cancel := kimiRequestSignal(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, oauthHost+"/api/oauth/device_authorization", strings.NewReader(formURLEncode(map[string]string{"client_id": kimiClientID})))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := kimiHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	bodyBytes, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		text := strings.TrimSpace(string(bodyBytes))
		if text != "" {
			return nil, fmt.Errorf("Kimi Code device authorization failed with status %d: %s", response.StatusCode, text)
		}
		return nil, fmt.Errorf("Kimi Code device authorization failed with status %d", response.StatusCode)
	}
	var jsonBody map[string]any
	if err := json.Unmarshal(bodyBytes, &jsonBody); err != nil {
		return nil, fmt.Errorf("Invalid Kimi Code device authorization response: %s", string(bodyBytes))
	}
	deviceCode, _ := jsonBody["device_code"].(string)
	userCode, _ := jsonBody["user_code"].(string)
	verificationURI, _ := jsonBody["verification_uri"].(string)
	verificationURIComplete, _ := jsonBody["verification_uri_complete"].(string)
	trustedComplete, okComplete := kimiTrustedHTTPURL(verificationURIComplete)
	trusted, okTrusted := kimiTrustedHTTPURL(verificationURI)
	if deviceCode == "" || userCode == "" || verificationURI == "" || verificationURIComplete == "" || !okComplete || !okTrusted {
		return nil, fmt.Errorf("Invalid Kimi Code device authorization response: %s", string(bodyBytes))
	}
	intervalSeconds := float64(kimiDefaultPollIntervalSec)
	if interval, ok := jsonBody["interval"].(float64); ok && interval > 0 && !isInfOrNaN(interval) {
		intervalSeconds = interval
	}
	expiresInSeconds := float64(kimiDeviceCodeTimeoutSec)
	if expires, ok := jsonBody["expires_in"].(float64); ok && expires > 0 && !isInfOrNaN(expires) {
		expiresInSeconds = expires
	}
	return &kimiDeviceAuthorization{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         trusted,
		VerificationURIComplete: trustedComplete,
		IntervalSeconds:         intervalSeconds,
		ExpiresInSeconds:        expiresInSeconds,
	}, nil
}

func isInfOrNaN(value float64) bool {
	return value != value || value > 1e308 || value < -1e308
}

func parseKimiTokenResponse(jsonBody map[string]any, operation string) (*kimiTokenResponse, error) {
	accessToken, _ := jsonBody["access_token"].(string)
	refreshToken, _ := jsonBody["refresh_token"].(string)
	expiresIn, expiresOK := jsonBody["expires_in"].(float64)
	encoded, _ := json.Marshal(jsonBody)
	if accessToken == "" || refreshToken == "" || !expiresOK || isInfOrNaN(expiresIn) || expiresIn <= 0 {
		return nil, fmt.Errorf("Kimi Code token %s response missing fields: %s", operation, string(encoded))
	}
	return &kimiTokenResponse{
		Access:  accessToken,
		Refresh: refreshToken,
		Expires: float64(time.Now().UnixMilli()) + expiresIn*1000,
	}, nil
}

func pollKimiToken(ctx context.Context, oauthHost string, device *kimiDeviceAuthorization, signal context.Context) (*kimiTokenResponse, error) {
	interval := device.IntervalSeconds
	expires := device.ExpiresInSeconds
	return PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[*kimiTokenResponse]{
		IntervalSeconds:     &interval,
		ExpiresInSeconds:    &expires,
		WaitBeforeFirstPoll: true,
		Signal:              signal,
		Poll: func() (OAuthDeviceCodePollResult[*kimiTokenResponse], error) {
			requestCtx, cancel := kimiRequestSignal(ctx)
			defer cancel()
			request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, oauthHost+"/api/oauth/token", strings.NewReader(formURLEncode(map[string]string{
				"client_id":   kimiClientID,
				"device_code": device.DeviceCode,
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			})))
			if err != nil {
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{}, err
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept", "application/json")
			response, err := kimiHTTPClient.Do(request)
			if err != nil {
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{}, err
			}
			defer response.Body.Close()
			if response.StatusCode >= 500 {
				body, _ := io.ReadAll(response.Body)
				text := strings.TrimSpace(string(body))
				message := fmt.Sprintf("Kimi Code device token request failed with status %d", response.StatusCode)
				if text != "" {
					message += ": " + text
				}
				return DeviceCodeFailed[*kimiTokenResponse](message), nil
			}
			jsonBody := kimiReadJSON(response)
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if _, ok := jsonBody["access_token"].(string); ok {
					token, parseErr := parseKimiTokenResponse(jsonBody, "poll")
					if parseErr != nil {
						return DeviceCodeFailed[*kimiTokenResponse](parseErr.Error()), nil
					}
					return DeviceCodeComplete(token), nil
				}
			}
			errorCode, _ := jsonBody["error"].(string)
			description := ""
			if text, ok := jsonBody["error_description"].(string); ok && text != "" {
				description = ": " + text
			}
			switch errorCode {
			case "authorization_pending":
				return DeviceCodePending[*kimiTokenResponse](), nil
			case "slow_down":
				var intervalPtr *float64
				if raw, ok := jsonBody["interval"].(float64); ok && raw > 0 {
					intervalPtr = &raw
				}
				return DeviceCodeSlowDown[*kimiTokenResponse](intervalPtr), nil
			case "expired_token":
				return DeviceCodeFailed[*kimiTokenResponse]("Kimi Code device authorization expired. Please restart login."), nil
			case "access_denied":
				return DeviceCodeFailed[*kimiTokenResponse]("Kimi Code login was denied."), nil
			}
			message := fmt.Sprintf("Kimi Code device token request failed (status %d)", response.StatusCode)
			if errorCode != "" {
				message += ": " + errorCode + description
			}
			return DeviceCodeFailed[*kimiTokenResponse](message), nil
		},
	})
}

func kimiRetryableRefreshFailure(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func refreshKimiToken(ctx context.Context, oauthHost, refreshToken string, signal context.Context) (*kimiTokenResponse, error) {
	var lastError error
	for attempt := 0; attempt <= kimiRefreshMaxRetries; attempt++ {
		if attempt > 0 {
			if err := utils.Sleep(signal, 1000*float64(int(1)<<uint(attempt-1))); err != nil {
				return nil, err
			}
		}
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Kimi Code token refresh aborted")
		}

		requestCtx, cancel := kimiRequestSignal(ctx)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, oauthHost+"/api/oauth/token", strings.NewReader(formURLEncode(map[string]string{
			"client_id":     kimiClientID,
			"grant_type":    "refresh_token",
			"refresh_token": refreshToken,
		})))
		if err != nil {
			cancel()
			lastError = err
			continue
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")
		response, err := kimiHTTPClient.Do(request)
		if err != nil {
			cancel()
			lastError = err
			continue
		}
		jsonBody := kimiReadJSON(response)
		status := response.StatusCode
		response.Body.Close()
		cancel()

		if status >= 200 && status < 300 {
			token, parseErr := parseKimiTokenResponse(jsonBody, "refresh")
			if parseErr != nil {
				return nil, parseErr
			}
			return token, nil
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden || jsonBody["error"] == "invalid_grant" {
			description := ""
			if text, ok := jsonBody["error_description"].(string); ok && text != "" {
				description = ": " + text
			}
			return nil, fmt.Errorf("Kimi Code token refresh unauthorized (status %d)%s", status, description)
		}
		if kimiRetryableRefreshFailure(status) && attempt < kimiRefreshMaxRetries {
			lastError = fmt.Errorf("Kimi Code token refresh failed with status %d", status)
			continue
		}
		encoded, _ := json.Marshal(jsonBody)
		return nil, fmt.Errorf("Kimi Code token refresh failed with status %d: %s", status, string(encoded))
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, errors.New("Kimi Code token refresh failed")
}

func loginKimiCoding(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	oauthHost := kimiOAuthHost()
	device, err := startKimiDeviceAuthorization(ctx, oauthHost, interaction.Signal())
	if err != nil {
		return nil, err
	}
	interaction.Notify(authtypes.AuthEvent{
		Type:             authtypes.AuthEventDeviceCode,
		UserCode:         &device.UserCode,
		VerificationURI:  &device.VerificationURIComplete,
		IntervalSeconds:  &device.IntervalSeconds,
		ExpiresInSeconds: &device.ExpiresInSeconds,
	})
	token, err := pollKimiToken(ctx, oauthHost, device, interaction.Signal())
	if err != nil {
		return nil, err
	}
	return &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{Access: token.Access, Refresh: token.Refresh, Expires: token.Expires},
		Type:             authtypes.CredentialTypeOAuth,
	}, nil
}

// KimiCodingOAuth is the Kimi Code (subscription) OAuth flow.
//
// Ports `kimiCodingOAuth` from packages/ai/src/auth/oauth/kimi-coding.ts.
var KimiCodingOAuth = &authtypes.OAuthAuth{
	Name:           "Kimi Code (subscription)",
	IsSubscription: true,
	LoginLabel:     "Sign in with Kimi Code",
	Login:          loginKimiCoding,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		token, err := refreshKimiToken(ctx, kimiOAuthHost(), credential.Refresh, ctx)
		if err != nil {
			return nil, err
		}
		return &authtypes.OAuthCredential{
			OAuthCredentials: authtypes.OAuthCredentials{Access: token.Access, Refresh: token.Refresh, Expires: token.Expires},
			Type:             authtypes.CredentialTypeOAuth,
		}, nil
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		bearer := "Bearer " + credential.Access
		return authtypes.ModelAuth{Headers: mapForProviderHeaders("Authorization", bearer)}, nil
	},
}

func mapForProviderHeaders(key, value string) map[string]*string {
	return map[string]*string{key: &value}
}
