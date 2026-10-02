// This file is a Go port of packages/ai/src/auth/oauth/radius.ts from Pi at
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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
)

const (
	radiusCallbackHost    = "127.0.0.1"
	radiusCallbackPort    = 1456
	radiusCallbackPath    = "/oauth/callback"
	radiusRedirectURI     = "http://127.0.0.1:1456/oauth/callback"
	radiusTokenExpirySkew = 60 * 1000
	radiusLoginBrowser    = "browser"
	radiusLoginDeviceCode = "device-code"
	radiusOAuthClientID   = "pi-gateway"
	radiusOAuthScope      = "gateway offline_access"
	radiusDeviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"
)

var radiusHTTPClient = &http.Client{Timeout: 60 * time.Second}

type radiusOAuthDiscovery struct {
	AuthorizationEndpoint string
}

func loadRadiusOAuthDiscovery(ctx context.Context, gateway string, signal context.Context) (*radiusOAuthDiscovery, error) {
	discoveryURL, err := url.Parse(gateway)
	if err != nil {
		return nil, err
	}
	discoveryURL.Path = "/v1/oauth"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	response, err := radiusHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Could not load Radius OAuth config from %s: %d %s", gateway, response.StatusCode, string(body))
	}
	var discovery radiusOAuthDiscovery
	if err := json.Unmarshal(body, &discovery); err != nil || discovery.AuthorizationEndpoint == "" {
		return nil, fmt.Errorf("Invalid Radius OAuth config from %s", gateway)
	}
	return &discovery, nil
}

// OAuthResponseError is a token endpoint error carrying the HTTP status and
// OAuth error code.
type OAuthResponseError struct {
	Status     int
	OAuthError string
	Message    string
}

// Error implements error.
func (e *OAuthResponseError) Error() string { return e.Message }

func readRadiusOAuthResponseError(response *http.Response, message string) *OAuthResponseError {
	body, _ := io.ReadAll(response.Body)
	var oauthError string
	var description string
	if len(strings.TrimSpace(string(body))) > 0 {
		var data struct {
			Error            any `json:"error"`
			ErrorDescription any `json:"error_description"`
		}
		if err := json.Unmarshal(body, &data); err == nil {
			if text, ok := data.Error.(string); ok {
				oauthError = text
			}
			if text, ok := data.ErrorDescription.(string); ok {
				description = text
			}
		} else {
			description = string(body)
		}
	}
	detail := ""
	switch {
	case oauthError != "" && description != "":
		detail = oauthError + ": " + description
	case oauthError != "":
		detail = oauthError
	case description != "":
		detail = description
	default:
		detail = itoa(response.StatusCode)
	}
	return &OAuthResponseError{Status: response.StatusCode, OAuthError: oauthError, Message: message + ": " + detail}
}

func requestRadiusOAuthToken(ctx context.Context, gateway string, form url.Values, signal context.Context) (*authtypes.OAuthCredential, error) {
	tokenURL, err := url.Parse(gateway)
	if err != nil {
		return nil, err
	}
	tokenURL.Path = "/v1/oauth/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := radiusHTTPClient.Do(request)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, readRadiusOAuthResponseError(response, "Radius OAuth token request failed")
	}
	var data struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
		Scope        *string `json:"scope"`
	}
	body, _ := io.ReadAll(response.Body)
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	credential := &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Access:  data.AccessToken,
			Refresh: data.RefreshToken,
			Expires: float64(time.Now().UnixMilli()) + data.ExpiresIn*1000 - radiusTokenExpirySkew,
		},
		Type: authtypes.CredentialTypeOAuth,
	}
	if data.Scope != nil {
		_ = credential.SetExtra("scope", *data.Scope)
	}
	return credential, nil
}

type radiusCallbackServer struct {
	waitForCode func() *string
	close       func()
}

func startRadiusOAuthCallbackServer(expectedState string, signal context.Context) (*radiusCallbackServer, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", radiusCallbackHost, radiusCallbackPort))
	if err != nil {
		return nil, err
	}
	resultCh := make(chan *string, 1)
	settled := false
	settle := func(code *string) {
		if settled {
			return
		}
		settled = true
		resultCh <- code
	}
	stopAbort := context.AfterFunc(signal, func() { settle(nil) })

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL, err := url.Parse(r.URL.String())
		if err != nil || requestURL.Path != radiusCallbackPath {
			writeHTML(w, http.StatusNotFound, OAuthErrorHTML("Callback route not found.", nil))
			return
		}
		if requestURL.Query().Get("state") != expectedState {
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("OAuth state mismatch.", nil))
			return
		}
		if oauthError := requestURL.Query().Get("error"); oauthError != "" {
			description := requestURL.Query().Get("error_description")
			if description == "" {
				description = oauthError
			}
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML(description, nil))
			settle(nil)
			return
		}
		code := requestURL.Query().Get("code")
		if code == "" {
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("Missing authorization code.", nil))
			return
		}
		writeHTML(w, http.StatusOK, OAuthSuccessHTML("Signed in to Radius. You may now close this page."))
		settle(&code)
	})

	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	return &radiusCallbackServer{
		waitForCode: func() *string { return <-resultCh },
		close: func() {
			stopAbort()
			settle(nil)
			_ = server.Close()
		},
	}, nil
}

func loginRadiusWithBrowser(ctx context.Context, gateway, authorizationEndpoint string, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	verifier, challenge := pkce.Verifier, pkce.Challenge
	state := randomUUID()
	authorize, err := url.Parse(authorizationEndpoint)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", radiusOAuthClientID)
	params.Set("redirect_uri", radiusRedirectURI)
	params.Set("scope", radiusOAuthScope)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("handoff", "url")
	params.Set("state", state)
	authorize.RawQuery = params.Encode()

	callbackServer, err := startRadiusOAuthCallbackServer(state, interaction.Signal())
	if err != nil {
		return nil, err
	}
	defer callbackServer.close()

	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: stringPtr("Listening for OAuth callback on " + radiusRedirectURI)})
	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventAuthURL, URL: stringPtr(authorize.String()), Instructions: stringPtr("Continue in your browser.")})

	code := callbackServer.waitForCode()
	if code == nil {
		if interaction.Signal() != nil && interaction.Signal().Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, errors.New("OAuth callback did not complete.")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", radiusOAuthClientID)
	form.Set("redirect_uri", radiusRedirectURI)
	form.Set("code", *code)
	form.Set("code_verifier", verifier)
	return requestRadiusOAuthToken(ctx, gateway, form, interaction.Signal())
}

type radiusDeviceAuthorizationResponse struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       float64
	Interval        *float64
}

func requestRadiusDeviceAuthorization(ctx context.Context, gateway string, signal context.Context) (*radiusDeviceAuthorizationResponse, error) {
	deviceURL, err := url.Parse(gateway)
	if err != nil {
		return nil, err
	}
	deviceURL.Path = "/v1/oauth/device"
	form := url.Values{}
	form.Set("client_id", radiusOAuthClientID)
	form.Set("scope", radiusOAuthScope)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := radiusHTTPClient.Do(request)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, readRadiusOAuthResponseError(response, "Radius OAuth device authorization failed")
	}
	var data struct {
		DeviceCode      string   `json:"device_code"`
		UserCode        string   `json:"user_code"`
		VerificationURI string   `json:"verification_uri"`
		ExpiresIn       float64  `json:"expires_in"`
		Interval        *float64 `json:"interval"`
	}
	body, _ := io.ReadAll(response.Body)
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if data.DeviceCode == "" || data.UserCode == "" || data.VerificationURI == "" || data.ExpiresIn == 0 {
		return nil, errors.New("Radius OAuth device authorization response is missing required fields")
	}
	return &radiusDeviceAuthorizationResponse{
		DeviceCode:      data.DeviceCode,
		UserCode:        data.UserCode,
		VerificationURI: data.VerificationURI,
		ExpiresIn:       data.ExpiresIn,
		Interval:        data.Interval,
	}, nil
}

func loginRadiusWithDeviceCode(ctx context.Context, gateway string, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	device, err := requestRadiusDeviceAuthorization(ctx, gateway, interaction.Signal())
	if err != nil {
		return nil, err
	}
	interaction.Notify(authtypes.AuthEvent{
		Type:             authtypes.AuthEventDeviceCode,
		UserCode:         &device.UserCode,
		VerificationURI:  &device.VerificationURI,
		IntervalSeconds:  device.Interval,
		ExpiresInSeconds: &device.ExpiresIn,
	})
	return PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[*authtypes.OAuthCredential]{
		IntervalSeconds:  device.Interval,
		ExpiresInSeconds: &device.ExpiresIn,
		Signal:           interaction.Signal(),
		Poll: func() (OAuthDeviceCodePollResult[*authtypes.OAuthCredential], error) {
			form := url.Values{}
			form.Set("grant_type", radiusDeviceCodeGrant)
			form.Set("client_id", radiusOAuthClientID)
			form.Set("device_code", device.DeviceCode)
			credentials, err := requestRadiusOAuthToken(ctx, gateway, form, interaction.Signal())
			if err != nil {
				var responseErr *OAuthResponseError
				if !errors.As(err, &responseErr) {
					return OAuthDeviceCodePollResult[*authtypes.OAuthCredential]{}, err
				}
				switch responseErr.OAuthError {
				case "authorization_pending":
					return DeviceCodePending[*authtypes.OAuthCredential](), nil
				case "slow_down":
					return DeviceCodeSlowDown[*authtypes.OAuthCredential](nil), nil
				case "expired_token":
					return DeviceCodeFailed[*authtypes.OAuthCredential]("Device authorization expired."), nil
				case "access_denied":
					return DeviceCodeFailed[*authtypes.OAuthCredential]("Device authorization was denied."), nil
				default:
					return OAuthDeviceCodePollResult[*authtypes.OAuthCredential]{}, err
				}
			}
			return DeviceCodeComplete(credentials), nil
		},
	})
}

// RadiusOAuthOptions configures a Radius gateway OAuth flow.
//
// Ports `RadiusOAuthOptions` from packages/ai/src/auth/oauth/radius.ts.
type RadiusOAuthOptions struct {
	Name    string
	Gateway string
}

// CreateRadiusOAuth builds the Radius gateway OAuth flow for a gateway.
//
// Ports `createRadiusOAuth` from packages/ai/src/auth/oauth/radius.ts.
func CreateRadiusOAuth(options RadiusOAuthOptions) *authtypes.OAuthAuth {
	gateway := catalog.NormalizeRadiusGatewayUrl(options.Gateway)
	return &authtypes.OAuthAuth{
		Name: options.Name,
		Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
			loginMethod, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSelect,
				Message: "Sign in to " + options.Name + ":",
				Options: []authtypes.AuthPromptOption{
					{ID: radiusLoginBrowser, Label: "Sign in with browser (recommended)"},
					{ID: radiusLoginDeviceCode, Label: "Sign in with device code (when signing in from another device)"},
				},
			})
			if err != nil {
				return nil, err
			}
			switch loginMethod {
			case radiusLoginDeviceCode:
				return loginRadiusWithDeviceCode(ctx, gateway, interaction)
			case radiusLoginBrowser:
				discovery, err := loadRadiusOAuthDiscovery(ctx, gateway, interaction.Signal())
				if err != nil {
					return nil, err
				}
				return loginRadiusWithBrowser(ctx, gateway, discovery.AuthorizationEndpoint, interaction)
			default:
				return nil, fmt.Errorf("Unknown %s sign-in method: %s", options.Name, loginMethod)
			}
		},
		Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
			form := url.Values{}
			form.Set("grant_type", "refresh_token")
			form.Set("client_id", radiusOAuthClientID)
			form.Set("refresh_token", credential.Refresh)
			return requestRadiusOAuthToken(ctx, gateway, form, ctx)
		},
		ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
			return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
		},
	}
}
