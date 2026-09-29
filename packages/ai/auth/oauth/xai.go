// This file is a Go port of packages/ai/src/auth/oauth/xai.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

const (
	xaiClientID               = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiScope                  = "openid profile email offline_access grok-cli:access api:access"
	xaiDeviceCodeURL          = "https://auth.x.ai/oauth2/device/code"
	xaiTokenURL               = "https://auth.x.ai/oauth2/token"
	xaiRefreshSkewMs          = 5 * 60 * 1000
	xaiDefaultLifetimeSeconds = 3600
)

var xaiHTTPClient = &http.Client{Timeout: 60 * time.Second}

type xaiHTTPResponse struct {
	OK     bool
	Status int
	Body   map[string]any
}

type xaiDeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete *string
	IntervalSeconds         *float64
	ExpiresInSeconds        float64
}

func xaiRequiredString(body map[string]any, field string) (string, error) {
	value, ok := body[field].(string)
	if !ok || value == "" {
		return "", errors.New("Invalid xAI OAuth response field: " + field)
	}
	return value, nil
}

func xaiPositiveNumber(body map[string]any, field string) (float64, error) {
	value, ok := body[field].(float64)
	if !ok || isInfOrNaN(value) || value <= 0 {
		return 0, errors.New("Invalid xAI OAuth response field: " + field)
	}
	return value, nil
}

func xaiValidateVerificationURI(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return "", errors.New("Untrusted verification URI in xAI OAuth response")
	}
	return parsed.String(), nil
}

func xaiPostForm(ctx context.Context, rawURL string, fields map[string]string, signal context.Context) (*xaiHTTPResponse, error) {
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := xaiHTTPClient.Do(request)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	body := map[string]any{}
	if len(bodyBytes) > 0 {
		var parsed any
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			if signal != nil && signal.Err() != nil {
				return nil, errors.New("Login cancelled")
			}
			return nil, errors.New("xAI OAuth returned invalid JSON (HTTP " + itoa(response.StatusCode) + ")")
		}
		if object, ok := parsed.(map[string]any); ok {
			body = object
		}
	}
	return &xaiHTTPResponse{OK: response.StatusCode >= 200 && response.StatusCode < 300, Status: response.StatusCode, Body: body}, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func xaiRequestFailure(action string, response *xaiHTTPResponse) error {
	errorCode, _ := response.Body["error"].(string)
	description, _ := response.Body["error_description"].(string)
	detail := strings.Trim(strings.Join(nonEmpty(errorCode, description), ": "), ": ")
	message := "xAI OAuth " + action + " failed (HTTP " + itoa(response.Status) + ")"
	if detail != "" {
		message += ": " + detail
	}
	return errors.New(message)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func xaiParseDeviceCode(body map[string]any) (*xaiDeviceCode, error) {
	var intervalSeconds *float64
	if interval, ok := body["interval"].(float64); ok && !isInfOrNaN(interval) && interval > 0 {
		intervalSeconds = &interval
	}
	var verificationURIComplete *string
	if raw, ok := body["verification_uri_complete"].(string); ok && raw != "" {
		validated, err := xaiValidateVerificationURI(raw)
		if err != nil {
			return nil, err
		}
		verificationURIComplete = &validated
	}
	deviceCode, err := xaiRequiredString(body, "device_code")
	if err != nil {
		return nil, err
	}
	userCode, err := xaiRequiredString(body, "user_code")
	if err != nil {
		return nil, err
	}
	verificationRaw, err := xaiRequiredString(body, "verification_uri")
	if err != nil {
		return nil, err
	}
	verificationURI, err := xaiValidateVerificationURI(verificationRaw)
	if err != nil {
		return nil, err
	}
	expiresIn, err := xaiPositiveNumber(body, "expires_in")
	if err != nil {
		return nil, err
	}
	return &xaiDeviceCode{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURIComplete,
		IntervalSeconds:         intervalSeconds,
		ExpiresInSeconds:        expiresIn,
	}, nil
}

func xaiCredentialsFromTokenResponse(body map[string]any, previousRefreshToken *string) (*authtypes.OAuthCredential, error) {
	access, err := xaiRequiredString(body, "access_token")
	if err != nil {
		return nil, err
	}
	refresh := ""
	if _, present := body["refresh_token"]; !present && previousRefreshToken != nil {
		refresh = *previousRefreshToken
	} else {
		refresh, err = xaiRequiredString(body, "refresh_token")
		if err != nil {
			return nil, err
		}
	}
	expiresInSeconds := float64(xaiDefaultLifetimeSeconds)
	if _, present := body["expires_in"]; present {
		expiresInSeconds, err = xaiPositiveNumber(body, "expires_in")
		if err != nil {
			return nil, err
		}
	}
	return &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Access:  access,
			Refresh: refresh,
			Expires: float64(time.Now().UnixMilli()) + expiresInSeconds*1000 - xaiRefreshSkewMs,
		},
		Type: authtypes.CredentialTypeOAuth,
	}, nil
}

func xaiRequestDeviceCode(ctx context.Context, signal context.Context) (*xaiDeviceCode, error) {
	response, err := xaiPostForm(ctx, xaiDeviceCodeURL, map[string]string{
		"client_id": xaiClientID,
		"scope":     xaiScope,
		"referrer":  "pi",
	}, signal)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, xaiRequestFailure("device authorization", response)
	}
	return xaiParseDeviceCode(response.Body)
}

func xaiPollForTokens(ctx context.Context, device *xaiDeviceCode, signal context.Context) (*authtypes.OAuthCredential, error) {
	expires := device.ExpiresInSeconds
	return PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[*authtypes.OAuthCredential]{
		IntervalSeconds:     device.IntervalSeconds,
		ExpiresInSeconds:    &expires,
		WaitBeforeFirstPoll: true,
		Signal:              signal,
		Poll: func() (OAuthDeviceCodePollResult[*authtypes.OAuthCredential], error) {
			response, err := xaiPostForm(ctx, xaiTokenURL, map[string]string{
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
				"client_id":   xaiClientID,
				"device_code": device.DeviceCode,
			}, signal)
			if err != nil {
				return OAuthDeviceCodePollResult[*authtypes.OAuthCredential]{}, err
			}
			if response.OK {
				credential, credErr := xaiCredentialsFromTokenResponse(response.Body, nil)
				if credErr != nil {
					return DeviceCodeFailed[*authtypes.OAuthCredential](credErr.Error()), nil
				}
				return DeviceCodeComplete(credential), nil
			}
			errorCode, _ := response.Body["error"].(string)
			switch errorCode {
			case "authorization_pending":
				return DeviceCodePending[*authtypes.OAuthCredential](), nil
			case "slow_down":
				var intervalPtr *float64
				if raw, ok := response.Body["interval"].(float64); ok {
					intervalPtr = &raw
				}
				return DeviceCodeSlowDown[*authtypes.OAuthCredential](intervalPtr), nil
			case "access_denied", "authorization_denied":
				return DeviceCodeFailed[*authtypes.OAuthCredential]("xAI device authorization was denied"), nil
			case "expired_token":
				return DeviceCodeFailed[*authtypes.OAuthCredential]("xAI device code expired"), nil
			}
			return DeviceCodeFailed[*authtypes.OAuthCredential](xaiRequestFailure("device token polling", response).Error()), nil
		},
	})
}

func loginXai(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	device, err := xaiRequestDeviceCode(ctx, interaction.Signal())
	if err != nil {
		return nil, err
	}
	verificationURI := device.VerificationURI
	if device.VerificationURIComplete != nil {
		verificationURI = *device.VerificationURIComplete
	}
	interaction.Notify(authtypes.AuthEvent{
		Type:             authtypes.AuthEventDeviceCode,
		UserCode:         &device.UserCode,
		VerificationURI:  &verificationURI,
		IntervalSeconds:  device.IntervalSeconds,
		ExpiresInSeconds: &device.ExpiresInSeconds,
	})
	return xaiPollForTokens(ctx, device, interaction.Signal())
}

func refreshXaiToken(ctx context.Context, refreshToken string, signal context.Context) (*authtypes.OAuthCredential, error) {
	response, err := xaiPostForm(ctx, xaiTokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     xaiClientID,
		"refresh_token": refreshToken,
	}, signal)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, xaiRequestFailure("token refresh", response)
	}
	return xaiCredentialsFromTokenResponse(response.Body, &refreshToken)
}

// XaiOAuth is the xAI (Grok/X subscription) OAuth flow.
//
// Ports `xaiOAuth` from packages/ai/src/auth/oauth/xai.ts.
var XaiOAuth = &authtypes.OAuthAuth{
	Name:           "xAI (Grok/X subscription)",
	IsSubscription: true,
	LoginLabel:     "Sign in with SuperGrok or X Premium",
	Login:          loginXai,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return refreshXaiToken(ctx, credential.Refresh, ctx)
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
	},
}
