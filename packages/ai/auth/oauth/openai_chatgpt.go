// This file is a Go port of packages/ai/src/auth/oauth/openai-chatgpt.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// OpenAI Responses API token sharing through Sign in with ChatGPT. This
// public-client flow uses no client secret and sends the resulting user access
// token directly to api.openai.com.
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
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const (
	// Every login registers a new client with this ID; OpenAI returns the
	// issued client ID in the callback.
	openAIChatGPTDynamicClientID  = "dynamic_agent_client"
	openAIChatGPTAgentNameHint    = "Pi"
	openAIChatGPTAuthorizeURL     = "https://auth.openai.com/api/accounts/authorize"
	openAIChatGPTTokenURL         = "https://auth.openai.com/api/accounts/oauth/token"
	openAIChatGPTResource         = "https://api.openai.com/v1"
	openAIChatGPTCallbackPort     = 1455
	openAIChatGPTCallbackPath     = "/auth/callback"
	openAIChatGPTRedirectURI      = "http://127.0.0.1:1455/auth/callback"
	openAIChatGPTDirectTokenScope = "chatgpt.tokens.use.direct"
	openAIChatGPTScope            = "openid profile email offline_access resource.invoke " + openAIChatGPTDirectTokenScope
	// Refresh this long before the real expiry so a request never starts with a
	// token about to expire.
	openAIChatGPTExpiryMarginMS = 3 * 60 * 1000
)

var openAIChatGPTUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// openaiChatGPTHTTPClient is replaceable in tests.
var openaiChatGPTHTTPClient = &http.Client{Timeout: 30 * time.Second}

func openAIChatGPTCallbackHost() string {
	if value := utils.GetProviderEnvValue("PI_OAUTH_CALLBACK_HOST", nil); value != nil && *value != "" {
		return *value
	}
	return "127.0.0.1"
}

type openAIChatGPTAuthorizationResult struct {
	Code     string
	ClientID string
}

func authorizationResultFromOpenAIChatGPTCallback(rawURL *url.URL, expectedState string) (*openAIChatGPTAuthorizationResult, error) {
	code := rawURL.Query().Get("code")
	if code == "" {
		return nil, errors.New("Missing authorization code")
	}
	state := rawURL.Query().Get("state")
	if state == "" {
		return nil, errors.New("Missing OAuth state")
	}
	if state != expectedState {
		return nil, errors.New("OAuth state mismatch")
	}
	clientID := strings.TrimSpace(rawURL.Query().Get("client_id"))
	if clientID == "" {
		return nil, errors.New("OpenAI OAuth registration callback did not contain an issued client ID")
	}
	return &openAIChatGPTAuthorizationResult{Code: code, ClientID: clientID}, nil
}

func authorizationResultFromOpenAIChatGPTManualInput(input, expectedState string) (*openAIChatGPTAuthorizationResult, error) {
	parsed, err := url.Parse(strings.TrimSpace(input))
	if err != nil || parsed.Scheme == "" {
		return nil, errors.New("Paste the full callback URL from the browser")
	}
	expected, _ := url.Parse(openAIChatGPTRedirectURI)
	if parsed.Scheme != expected.Scheme || parsed.Host != expected.Host || parsed.Path != expected.Path {
		return nil, fmt.Errorf("The pasted callback URL must start with %s", openAIChatGPTRedirectURI)
	}
	if providerError := parsed.Query().Get("error"); providerError != "" {
		return nil, fmt.Errorf("ChatGPT authorization failed: %s", providerError)
	}
	return authorizationResultFromOpenAIChatGPTCallback(parsed, expectedState)
}

func openAIChatGPTRequestToken(ctx context.Context, form url.Values) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatGPTTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := openaiChatGPTHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		text := strings.TrimSpace(string(body))
		if text == "" {
			text = response.Status
		}
		return nil, fmt.Errorf("OpenAI OAuth token request failed (%d): %s", response.StatusCode, text)
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, errors.New("OpenAI OAuth token response must be an object")
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("OpenAI OAuth token response must be an object")
	}
	return object, nil
}

func openAIChatGPTTokenString(token map[string]any, field string) (string, error) {
	value, ok := token[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("OpenAI OAuth token response has invalid %s", field)
	}
	return value, nil
}

func credentialFromOpenAIChatGPTTokenResponse(token map[string]any, clientID string) (*authtypes.OAuthCredential, error) {
	access, err := openAIChatGPTTokenString(token, "access_token")
	if err != nil {
		return nil, err
	}
	refresh, err := openAIChatGPTTokenString(token, "refresh_token")
	if err != nil {
		return nil, err
	}
	scope, err := openAIChatGPTTokenString(token, "scope")
	if err != nil {
		return nil, err
	}
	expiresIn, ok := token["expires_in"].(float64)
	if !ok || math.IsNaN(expiresIn) || math.IsInf(expiresIn, 0) || expiresIn <= 0 {
		return nil, errors.New("OpenAI OAuth token response has invalid expires_in")
	}
	scopes := strings.Fields(scope)
	hasDirectTokenScope := false
	for _, granted := range scopes {
		if granted == openAIChatGPTDirectTokenScope {
			hasDirectTokenScope = true
			break
		}
	}
	if !hasDirectTokenScope {
		return nil, fmt.Errorf("OpenAI OAuth grant did not include %s", openAIChatGPTDirectTokenScope)
	}
	now := float64(time.Now().UnixMilli())
	credential := &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Access:  access,
			Refresh: refresh,
			Expires: now + expiresIn*1000 - openAIChatGPTExpiryMarginMS,
		},
		Type: authtypes.CredentialTypeOAuth,
	}
	if err := credential.SetExtra("clientId", clientID); err != nil {
		return nil, err
	}
	if err := credential.SetExtra("scopes", scopes); err != nil {
		return nil, err
	}
	return credential, nil
}

func exchangeOpenAIChatGPTAuthorizationCode(ctx context.Context, code, verifier, clientID string) (*authtypes.OAuthCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", openAIChatGPTRedirectURI)
	form.Set("resource", openAIChatGPTResource)
	token, err := openAIChatGPTRequestToken(ctx, form)
	if err != nil {
		return nil, err
	}
	// Pi does not use the ID token to identify the user or read profile data.
	// Keep the presence check as part of the token-response contract.
	if idToken, ok := token["id_token"].(string); !ok || strings.TrimSpace(idToken) == "" {
		return nil, errors.New("OpenAI OAuth token response did not contain an ID token")
	}
	return credentialFromOpenAIChatGPTTokenResponse(token, clientID)
}

func refreshOpenAIChatGPTAccessToken(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
	clientID, ok := credential.ExtraString("clientId")
	if !ok || strings.TrimSpace(clientID) == "" {
		return nil, errors.New("Stored OpenAI OAuth credential does not contain an issued client ID; reconnect ChatGPT")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", credential.Refresh)
	form.Set("resource", openAIChatGPTResource)
	token, err := openAIChatGPTRequestToken(ctx, form)
	if err != nil {
		return nil, err
	}
	return credentialFromOpenAIChatGPTTokenResponse(token, clientID)
}

// agentHostID returns OpenAI's stable installation URI (`urn:uuid:<uuid>`).
func openAIChatGPTAgentHostID(deviceID string) (string, error) {
	if deviceID == "" || !openAIChatGPTUUIDPattern.MatchString(deviceID) {
		return "", errors.New("Sign in with ChatGPT requires a device ID (UUID) for this installation")
	}
	return "urn:uuid:" + strings.ToLower(deviceID), nil
}

type openAIChatGPTAuthOutcome struct {
	result *openAIChatGPTAuthorizationResult
	err    error
}

type openAIChatGPTCallbackServer struct {
	server   *http.Server
	listener net.Listener
	resultCh chan openAIChatGPTAuthOutcome

	mu      sync.Mutex
	settled bool
}

func (c *openAIChatGPTCallbackServer) settle(outcome openAIChatGPTAuthOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled {
		return
	}
	c.settled = true
	c.resultCh <- outcome
}

func (c *openAIChatGPTCallbackServer) close() {
	if c.server != nil {
		_ = c.server.Close()
	}
	if c.listener != nil {
		_ = c.listener.Close()
	}
}

func startOpenAIChatGPTCallbackServer(expectedState string) (*openAIChatGPTCallbackServer, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(openAIChatGPTCallbackHost(), strconv.Itoa(openAIChatGPTCallbackPort)))
	if err != nil {
		return nil, err
	}
	server := &openAIChatGPTCallbackServer{
		listener: listener,
		resultCh: make(chan openAIChatGPTAuthOutcome, 1),
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL := r.URL
		if requestURL.Path != openAIChatGPTCallbackPath {
			sendCallbackPage(w, http.StatusNotFound, utils.OAuthErrorHTML("Callback route not found.", nil))
			return
		}
		if providerError := requestURL.Query().Get("error"); providerError != "" {
			detail := "Error: " + providerError
			sendCallbackPage(w, http.StatusBadRequest, utils.OAuthErrorHTML("ChatGPT was not connected.", &detail))
			server.settle(openAIChatGPTAuthOutcome{err: fmt.Errorf("ChatGPT authorization failed: %s", providerError)})
			return
		}
		result, err := authorizationResultFromOpenAIChatGPTCallback(requestURL, expectedState)
		if err != nil {
			sendCallbackPage(w, http.StatusBadRequest, utils.OAuthErrorHTML(err.Error(), nil))
			return
		}
		sendCallbackPage(w, http.StatusOK, utils.OAuthSuccessHTML("ChatGPT authentication completed. You can close this window."))
		server.settle(openAIChatGPTAuthOutcome{result: result})
	})
	server.server = &http.Server{Handler: handler}
	go func() {
		_ = server.server.Serve(listener)
	}()
	return server, nil
}

func loginOpenAIChatGPT(ctx context.Context, interaction authtypes.ProviderAuthInteraction, options *authtypes.LoginOptions) (*authtypes.OAuthCredential, error) {
	deviceID := ""
	if options != nil && options.GetDeviceID != nil {
		deviceID = options.GetDeviceID()
	}
	hostID, err := openAIChatGPTAgentHostID(deviceID)
	if err != nil {
		return nil, err
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	verifier, challenge := pkce.Verifier, pkce.Challenge
	state, err := randomBase64URL(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBase64URL(32)
	if err != nil {
		return nil, err
	}

	server, serverErr := startOpenAIChatGPTCallbackServer(state)
	if serverErr != nil {
		message := fmt.Sprintf("Could not listen on %s; paste the final redirect URL to continue. %s", openAIChatGPTRedirectURI, serverErr.Error())
		interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventInfo, Message: &message})
	} else {
		defer server.close()
	}

	authorize, err := url.Parse(openAIChatGPTAuthorizeURL)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("client_id", openAIChatGPTDynamicClientID)
	params.Set("agent_name_hint", openAIChatGPTAgentNameHint)
	params.Set("ext_agent_host_id", hostID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", openAIChatGPTRedirectURI)
	params.Set("resource", openAIChatGPTResource)
	params.Set("scope", openAIChatGPTScope)
	params.Set("state", state)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("nonce", nonce)
	authorize.RawQuery = params.Encode()
	instructions := "Complete sign-in in your browser. If the callback does not complete, paste the final redirect URL here."
	interaction.Notify(authtypes.AuthEvent{
		Type:         authtypes.AuthEventAuthURL,
		URL:          stringPtr(authorize.String()),
		Instructions: &instructions,
	})

	manualCtx, cancelManual := context.WithCancel(interaction.Signal())
	defer cancelManual()
	manualCh := make(chan openAIChatGPTAuthOutcome, 1)
	placeholder := openAIChatGPTRedirectURI
	go func() {
		input, promptErr := interaction.Prompt(manualCtx, authtypes.AuthPrompt{
			Type:        authtypes.AuthPromptManualCode,
			Message:     "Complete login in your browser, or paste the final redirect URL here:",
			Placeholder: &placeholder,
		})
		var result *openAIChatGPTAuthorizationResult
		if promptErr == nil {
			result, promptErr = authorizationResultFromOpenAIChatGPTManualInput(input, state)
		}
		manualCh <- openAIChatGPTAuthOutcome{result: result, err: promptErr}
	}()

	var authorization *openAIChatGPTAuthorizationResult
	if server != nil {
		select {
		case outcome := <-server.resultCh:
			if outcome.err != nil {
				return nil, outcome.err
			}
			authorization = outcome.result
		case outcome := <-manualCh:
			if outcome.err != nil {
				if interaction.Signal() != nil && interaction.Signal().Err() != nil {
					return nil, errors.New("Login cancelled")
				}
				return nil, outcome.err
			}
			authorization = outcome.result
		}
	} else {
		outcome := <-manualCh
		if outcome.err != nil {
			if interaction.Signal() != nil && interaction.Signal().Err() != nil {
				return nil, errors.New("Login cancelled")
			}
			return nil, outcome.err
		}
		authorization = outcome.result
	}

	if authorization == nil {
		return nil, errors.New("Missing authorization code")
	}
	progress := "Exchanging authorization code for tokens..."
	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: &progress})
	return exchangeOpenAIChatGPTAuthorizationCode(ctx, authorization.Code, verifier, authorization.ClientID)
}

// OpenAIChatGPTOAuth is the OpenAI (ChatGPT subscription) OAuth flow.
//
// Ports `openaiChatGPTOAuth` from
// packages/ai/src/auth/oauth/openai-chatgpt.ts.
var OpenAIChatGPTOAuth = &authtypes.OAuthAuth{
	Name:           "OpenAI (ChatGPT subscription)",
	IsSubscription: true,
	LoginLabel:     "Sign in with ChatGPT",
	Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
		return loginOpenAIChatGPT(ctx, interaction, nil)
	},
	LoginWithOptions: loginOpenAIChatGPT,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return refreshOpenAIChatGPTAccessToken(ctx, credential)
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
	},
}
