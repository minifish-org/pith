// This file is a Go port of packages/ai/src/auth/oauth/anthropic.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"encoding/base64"
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
	"github.com/minifish-org/pith/packages/ai/utils"
)

const (
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicTokenURL     = "https://platform.claude.com/v1/oauth/token"
	anthropicCallbackPort = 53692
	anthropicCallbackPath = "/callback"
	anthropicRedirectURI  = "http://localhost:53692/callback"
	anthropicScopes       = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
)

var anthropicClientID = func() string {
	decoded, err := base64.StdEncoding.DecodeString("OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl")
	if err != nil {
		return ""
	}
	return string(decoded)
}()

// anthropicHTTPClient is replaceable in tests.
var anthropicHTTPClient = &http.Client{Timeout: 30 * time.Second}

// startAnthropicServer is replaceable in tests.
var startAnthropicServer = startAnthropicCallbackServer

func anthropicCallbackHost() string {
	if value := utils.GetProviderEnvValue("PI_OAUTH_CALLBACK_HOST", nil); value != nil && *value != "" {
		return *value
	}
	return "127.0.0.1"
}

type anthropicAuthCode struct {
	Code  string
	State string
}

type anthropicCallbackServer struct {
	server      *http.Server
	redirectURI string
	cancelWait  func()
	waitForCode func() *anthropicAuthCode
}

func startAnthropicCallbackServer(ctx context.Context, expectedState string) (*anthropicCallbackServer, error) {
	host := anthropicCallbackHost()
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, anthropicCallbackPort))
	if err != nil {
		return nil, err
	}

	var settle func(*anthropicAuthCode)
	resultCh := make(chan *anthropicAuthCode, 1)
	settled := false
	settle = func(value *anthropicAuthCode) {
		if settled {
			return
		}
		settled = true
		resultCh <- value
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL, parseErr := url.Parse(r.URL.String())
		if parseErr != nil {
			writeHTML(w, http.StatusInternalServerError, OAuthErrorHTML("Internal error", nil))
			return
		}
		if requestURL.Path != anthropicCallbackPath {
			writeHTML(w, http.StatusNotFound, OAuthErrorHTML("Callback route not found.", nil))
			return
		}
		code := requestURL.Query().Get("code")
		state := requestURL.Query().Get("state")
		oauthErr := requestURL.Query().Get("error")

		if oauthErr != "" {
			detail := "Error: " + oauthErr
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("Anthropic authentication did not complete.", &detail))
			return
		}
		if code == "" || state == "" {
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("Missing code or state parameter.", nil))
			return
		}
		if state != expectedState {
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("State mismatch.", nil))
			return
		}
		writeHTML(w, http.StatusOK, OAuthSuccessHTML("Anthropic authentication completed. You can close this window."))
		settle(&anthropicAuthCode{Code: code, State: state})
	})

	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()

	return &anthropicCallbackServer{
		server:      server,
		redirectURI: anthropicRedirectURI,
		cancelWait:  func() { settle(nil) },
		waitForCode: func() *anthropicAuthCode { return <-resultCh },
	}, nil
}

func writeHTML(w http.ResponseWriter, status int, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, html)
}

func parseAnthropicAuthorizationInput(input string) (code *string, state *string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return nil, nil
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		q := parsed.Query()
		if c := q.Get("code"); c != "" {
			code = &c
		}
		if s := q.Get("state"); s != "" {
			state = &s
		}
		return code, state
	}
	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		c := parts[0]
		code = &c
		if len(parts) == 2 {
			s := parts[1]
			state = &s
		}
		return code, state
	}
	if strings.Contains(value, "code=") {
		params, err := url.ParseQuery(value)
		if err == nil {
			if c := params.Get("code"); c != "" {
				code = &c
			}
			if s := params.Get("state"); s != "" {
				state = &s
			}
			return code, state
		}
	}
	c := value
	return &c, nil
}

func formatErrorDetails(err error) string {
	if err == nil {
		return ""
	}
	details := []string{fmt.Sprintf("%T: %s", err, err.Error())}
	return strings.Join(details, "; ")
}

func anthropicPostJSON(ctx context.Context, url string, body map[string]any) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := anthropicHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", response.StatusCode, url, string(responseBody))
	}
	return string(responseBody), nil
}

func exchangeAnthropicAuthorizationCode(ctx context.Context, code, state, verifier, redirectURI string, signal context.Context) (*authtypes.OAuthCredential, error) {
	responseBody, err := anthropicPostJSON(ctx, anthropicTokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return nil, fmt.Errorf("Token exchange request failed. url=%s; redirect_uri=%s; response_type=authorization_code; details=%s", anthropicTokenURL, redirectURI, formatErrorDetails(err))
	}
	var tokenData struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(responseBody), &tokenData); err != nil {
		return nil, fmt.Errorf("Token exchange returned invalid JSON. url=%s; body=%s; details=%s", anthropicTokenURL, responseBody, formatErrorDetails(err))
	}
	now := float64(time.Now().UnixMilli())
	return &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Refresh: tokenData.RefreshToken,
			Access:  tokenData.AccessToken,
			Expires: now + tokenData.ExpiresIn*1000 - 5*60*1000,
		},
		Type: authtypes.CredentialTypeOAuth,
	}, nil
}

func loginAnthropic(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	verifier := pkce.Verifier
	server, err := startAnthropicServer(ctx, verifier)
	if err != nil {
		return nil, err
	}
	defer server.server.Close()

	manualCtx, cancelManual := context.WithCancel(interaction.Signal())
	defer cancelManual()
	stopAbort := context.AfterFunc(interaction.Signal(), func() { server.cancelWait() })
	defer stopAbort()

	authParams := url.Values{}
	authParams.Set("code", "true")
	authParams.Set("client_id", anthropicClientID)
	authParams.Set("response_type", "code")
	authParams.Set("redirect_uri", anthropicRedirectURI)
	authParams.Set("scope", anthropicScopes)
	authParams.Set("code_challenge", pkce.Challenge)
	authParams.Set("code_challenge_method", "S256")
	authParams.Set("state", verifier)
	passwordPlaceholder := anthropicRedirectURI
	interaction.Notify(authtypes.AuthEvent{
		Type:         authtypes.AuthEventAuthURL,
		URL:          stringPtr(anthropicAuthorizeURL + "?" + authParams.Encode()),
		Instructions: stringPtr("Complete login in your browser. If the browser is on another machine, paste the final redirect URL here."),
	})

	type manualResult struct {
		input *string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	go func() {
		message := "Complete login in your browser, or paste the authorization code / redirect URL here:"
		input, promptErr := interaction.Prompt(manualCtx, authtypes.AuthPrompt{
			Type:        authtypes.AuthPromptManualCode,
			Message:     message,
			Placeholder: &passwordPlaceholder,
		})
		manualCh <- manualResult{input: &input, err: promptErr}
		server.cancelWait()
	}()

	var code, state string
	var manualInput *string
	var manualErr error
	result := server.waitForCode()
	if result != nil {
		code = result.Code
		state = result.State
	} else {
		manual := <-manualCh
		manualInput = manual.input
		manualErr = manual.err
	}

	applyManual := func() {
		if manualInput == nil {
			return
		}
		parsedCode, parsedState := parseAnthropicAuthorizationInput(*manualInput)
		if parsedState != nil && *parsedState != verifier {
			manualErr = errors.New("OAuth state mismatch")
			return
		}
		if parsedCode != nil {
			code = *parsedCode
		}
		if parsedState != nil {
			state = *parsedState
		} else {
			state = verifier
		}
	}

	if code == "" {
		if manualErr != nil {
			return nil, manualErr
		}
		applyManual()
		if manualErr != nil {
			return nil, manualErr
		}
	}

	if code == "" {
		return nil, errors.New("Missing authorization code")
	}
	if state == "" {
		return nil, errors.New("Missing OAuth state")
	}
	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: stringPtr("Exchanging authorization code for tokens...")})
	return exchangeAnthropicAuthorizationCode(ctx, code, state, verifier, anthropicRedirectURI, interaction.Signal())
}

func refreshAnthropicToken(ctx context.Context, refreshToken string, signal context.Context) (*authtypes.OAuthCredential, error) {
	responseBody, err := anthropicPostJSON(ctx, anthropicTokenURL, map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     anthropicClientID,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("Anthropic token refresh request failed. url=%s; details=%s", anthropicTokenURL, formatErrorDetails(err))
	}
	var data struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
		Scope        *string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(responseBody), &data); err != nil {
		return nil, fmt.Errorf("Anthropic token refresh returned invalid JSON. url=%s; body=%s; details=%s", anthropicTokenURL, responseBody, formatErrorDetails(err))
	}
	now := float64(time.Now().UnixMilli())
	return &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Refresh: data.RefreshToken,
			Access:  data.AccessToken,
			Expires: now + data.ExpiresIn*1000 - 5*60*1000,
		},
		Type: authtypes.CredentialTypeOAuth,
	}, nil
}

// AnthropicOAuth is the Anthropic (Claude Pro/Max) OAuth flow.
//
// Ports `anthropicOAuth` from packages/ai/src/auth/oauth/anthropic.ts.
var AnthropicOAuth = &authtypes.OAuthAuth{
	Name:           "Anthropic (Claude Pro/Max)",
	IsSubscription: true,
	Login:          loginAnthropic,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return refreshAnthropicToken(ctx, credential.Refresh, ctx)
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
	},
}

func stringPtr(value string) *string { return &value }
