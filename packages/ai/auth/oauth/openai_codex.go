// This file is a Go port of packages/ai/src/auth/oauth/openai-codex.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const (
	codexClientID             = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthBaseURL          = "https://auth.openai.com"
	codexAuthorizeURL         = codexAuthBaseURL + "/oauth/authorize"
	codexTokenURL             = codexAuthBaseURL + "/oauth/token"
	codexRedirectURI          = "http://localhost:1455/auth/callback"
	codexDeviceUserCodeURL    = codexAuthBaseURL + "/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL       = codexAuthBaseURL + "/api/accounts/deviceauth/token"
	codexDeviceVerification   = codexAuthBaseURL + "/codex/device"
	codexDeviceRedirectURI    = codexAuthBaseURL + "/deviceauth/callback"
	codexDeviceCodeTimeoutSec = 15 * 60
	codexBrowserLoginMethod   = "browser"
	codexDeviceLoginMethod    = "device_code"
	codexScope                = "openid profile email offline_access"
	codexJWTClaimPath         = "https://api.openai.com/auth"
)

var codexHTTPClient = &http.Client{Timeout: 30 * time.Second}

func codexCallbackHost() string {
	if value := utils.GetProviderEnvValue("PI_OAUTH_CALLBACK_HOST", nil); value != nil && *value != "" {
		return *value
	}
	return "127.0.0.1"
}

type codexDeviceAuthInfo struct {
	DeviceAuthID    string
	UserCode        string
	IntervalSeconds float64
}

type codexDeviceTokenSuccess struct {
	AuthorizationCode string
	CodeVerifier      string
}

func createCodexState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func parseCodexAuthorizationInput(input string) (code *string, state *string) {
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

func decodeJWT(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil
		}
	}
	var result map[string]any
	if err := json.Unmarshal(decoded, &result); err != nil {
		return nil
	}
	return result
}

func getCodexAccountID(accessToken string) (string, bool) {
	payload := decodeJWT(accessToken)
	if payload == nil {
		return "", false
	}
	auth, ok := payload[codexJWTClaimPath].(map[string]any)
	if !ok {
		return "", false
	}
	accountID, ok := auth["chatgpt_account_id"].(string)
	if !ok || accountID == "" {
		return "", false
	}
	return accountID, true
}

type codexToken struct {
	Access  string
	Refresh string
	Expires float64
}

func readCodexTokenResponse(response *http.Response, operation string) (*codexToken, error) {
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI Codex token %s failed (%d): %s", operation, response.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  *string  `json:"access_token"`
		RefreshToken *string  `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", operation, string(body))
	}
	if payload.AccessToken == nil || payload.RefreshToken == nil || payload.ExpiresIn == nil {
		return nil, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", operation, string(body))
	}
	return &codexToken{
		Access:  *payload.AccessToken,
		Refresh: *payload.RefreshToken,
		Expires: float64(time.Now().UnixMilli()) + *payload.ExpiresIn*1000,
	}, nil
}

func exchangeCodexAuthorizationCode(ctx context.Context, code, verifier, redirectURI string, signal context.Context) (*codexToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", codexClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := codexHTTPClient.Do(request)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	return readCodexTokenResponse(response, "exchange")
}

func refreshCodexAccessToken(ctx context.Context, refreshToken string) (*codexToken, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", codexClientID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := codexHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("OpenAI Codex token refresh error: %s", err.Error())
	}
	defer response.Body.Close()
	return readCodexTokenResponse(response, "refresh")
}

func startCodexDeviceAuth(ctx context.Context, signal context.Context) (*codexDeviceAuthInfo, error) {
	payload, _ := json.Marshal(map[string]any{"client_id": codexClientID})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceUserCodeURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := codexHTTPClient.Do(request)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == 404 {
			return nil, errors.New("OpenAI Codex device code login is not enabled for this server. Use browser login or verify the server URL.")
		}
		return nil, fmt.Errorf("OpenAI Codex device code request failed with status %d: %s", response.StatusCode, string(body))
	}
	var raw struct {
		DeviceAuthID *string         `json:"device_auth_id"`
		UserCode     *string         `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", string(body))
	}
	var intervalSeconds float64
	if len(raw.Interval) > 0 {
		var number float64
		if err := json.Unmarshal(raw.Interval, &number); err == nil {
			intervalSeconds = number
		} else {
			var text string
			if err := json.Unmarshal(raw.Interval, &text); err == nil {
				parsed, perr := strconv.ParseFloat(strings.TrimSpace(text), 64)
				if perr != nil {
					return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", string(body))
				}
				intervalSeconds = parsed
			} else {
				return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", string(body))
			}
		}
	}
	if raw.DeviceAuthID == nil || *raw.DeviceAuthID == "" || raw.UserCode == nil || *raw.UserCode == "" || intervalSeconds < 0 {
		return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", string(body))
	}
	return &codexDeviceAuthInfo{DeviceAuthID: *raw.DeviceAuthID, UserCode: *raw.UserCode, IntervalSeconds: intervalSeconds}, nil
}

func pollCodexDeviceAuth(ctx context.Context, device *codexDeviceAuthInfo, signal context.Context) (*codexDeviceTokenSuccess, error) {
	interval := device.IntervalSeconds
	expires := float64(codexDeviceCodeTimeoutSec)
	result, err := PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[*codexDeviceTokenSuccess]{
		IntervalSeconds:  &interval,
		ExpiresInSeconds: &expires,
		Signal:           signal,
		Poll: func() (OAuthDeviceCodePollResult[*codexDeviceTokenSuccess], error) {
			payload, _ := json.Marshal(map[string]any{
				"device_auth_id": device.DeviceAuthID,
				"user_code":      device.UserCode,
			})
			request, rerr := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceTokenURL, strings.NewReader(string(payload)))
			if rerr != nil {
				return OAuthDeviceCodePollResult[*codexDeviceTokenSuccess]{}, rerr
			}
			request.Header.Set("Content-Type", "application/json")
			response, rerr := codexHTTPClient.Do(request)
			if rerr != nil {
				if signal != nil && signal.Err() != nil {
					return OAuthDeviceCodePollResult[*codexDeviceTokenSuccess]{}, errors.New("Login cancelled")
				}
				return OAuthDeviceCodePollResult[*codexDeviceTokenSuccess]{}, rerr
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				var raw struct {
					AuthorizationCode *string `json:"authorization_code"`
					CodeVerifier      *string `json:"code_verifier"`
				}
				if err := json.Unmarshal(body, &raw); err != nil || raw.AuthorizationCode == nil || raw.CodeVerifier == nil {
					return DeviceCodeFailed[*codexDeviceTokenSuccess](fmt.Sprintf("Invalid OpenAI Codex device auth token response: %s", string(body))), nil
				}
				return DeviceCodeComplete(&codexDeviceTokenSuccess{AuthorizationCode: *raw.AuthorizationCode, CodeVerifier: *raw.CodeVerifier}), nil
			}
			if response.StatusCode == 403 || response.StatusCode == 404 {
				return DeviceCodePending[*codexDeviceTokenSuccess](), nil
			}
			var errorCode any
			var parsed struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(body, &parsed) == nil && len(parsed.Error) > 0 {
				var text string
				if json.Unmarshal(parsed.Error, &text) == nil {
					errorCode = text
				} else {
					var object struct {
						Code *string `json:"code"`
					}
					if json.Unmarshal(parsed.Error, &object) == nil && object.Code != nil {
						errorCode = *object.Code
					}
				}
			}
			switch errorCode {
			case "deviceauth_authorization_pending":
				return DeviceCodePending[*codexDeviceTokenSuccess](), nil
			case "slow_down":
				return DeviceCodeSlowDown[*codexDeviceTokenSuccess](nil), nil
			}
			return DeviceCodeFailed[*codexDeviceTokenSuccess](fmt.Sprintf("OpenAI Codex device auth failed with status %d: %s", response.StatusCode, string(body))), nil
		},
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type codexAuthorizationFlow struct {
	Verifier string
	State    string
	URL      string
}

func createCodexAuthorizationFlow(originator string) (*codexAuthorizationFlow, error) {
	if originator == "" {
		originator = "pi"
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state, err := createCodexState()
	if err != nil {
		return nil, err
	}
	authorize, err := url.Parse(codexAuthorizeURL)
	if err != nil {
		return nil, err
	}
	query := authorize.Query()
	query.Set("response_type", "code")
	query.Set("client_id", codexClientID)
	query.Set("redirect_uri", codexRedirectURI)
	query.Set("scope", codexScope)
	query.Set("code_challenge", pkce.Challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", originator)
	authorize.RawQuery = query.Encode()
	return &codexAuthorizationFlow{Verifier: pkce.Verifier, State: state, URL: authorize.String()}, nil
}

type codexLocalServer struct {
	close       func()
	cancelWait  func()
	waitForCode func() *string
}

func startCodexLocalServer(state string) *codexLocalServer {
	resultCh := make(chan *string, 1)
	settled := false
	settle := func(value *string) {
		if settled {
			return
		}
		settled = true
		resultCh <- value
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL, err := url.Parse(r.URL.String())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			writeHTML(w, http.StatusInternalServerError, OAuthErrorHTML("Internal error while processing OAuth callback.", nil))
			return
		}
		if requestURL.Path != "/auth/callback" {
			writeHTML(w, http.StatusNotFound, OAuthErrorHTML("Callback route not found.", nil))
			return
		}
		if requestURL.Query().Get("state") != state {
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("State mismatch.", nil))
			return
		}
		code := requestURL.Query().Get("code")
		if code == "" {
			writeHTML(w, http.StatusBadRequest, OAuthErrorHTML("Missing authorization code.", nil))
			return
		}
		writeHTML(w, http.StatusOK, OAuthSuccessHTML("OpenAI authentication completed. You can close this window."))
		settle(&code)
	})

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:1455", codexCallbackHost()))
	if err != nil {
		return &codexLocalServer{
			close:       func() {},
			cancelWait:  func() { settle(nil) },
			waitForCode: func() *string { return nil },
		}
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	return &codexLocalServer{
		close: func() {
			_ = server.Close()
		},
		cancelWait:  func() { settle(nil) },
		waitForCode: func() *string { return <-resultCh },
	}
}

func credentialsFromCodexToken(token *codexToken) (*authtypes.OAuthCredential, error) {
	accountID, ok := getCodexAccountID(token.Access)
	if !ok {
		return nil, errors.New("Failed to extract accountId from token")
	}
	credential := &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Access:  token.Access,
			Refresh: token.Refresh,
			Expires: token.Expires,
		},
		Type: authtypes.CredentialTypeOAuth,
	}
	_ = credential.SetExtra("accountId", accountID)
	return credential, nil
}

func exchangeCodexCodeForCredentials(ctx context.Context, code, verifier, redirectURI string, signal context.Context) (*authtypes.OAuthCredential, error) {
	token, err := exchangeCodexAuthorizationCode(ctx, code, verifier, redirectURI, signal)
	if err != nil {
		return nil, err
	}
	return credentialsFromCodexToken(token)
}

func loginCodexDeviceCode(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	device, err := startCodexDeviceAuth(ctx, interaction.Signal())
	if err != nil {
		return nil, err
	}
	verificationURI := codexDeviceVerification
	interval := device.IntervalSeconds
	expires := float64(codexDeviceCodeTimeoutSec)
	interaction.Notify(authtypes.AuthEvent{
		Type:             authtypes.AuthEventDeviceCode,
		UserCode:         &device.UserCode,
		VerificationURI:  &verificationURI,
		IntervalSeconds:  &interval,
		ExpiresInSeconds: &expires,
	})
	token, err := pollCodexDeviceAuth(ctx, device, interaction.Signal())
	if err != nil {
		return nil, err
	}
	return exchangeCodexCodeForCredentials(ctx, token.AuthorizationCode, token.CodeVerifier, codexDeviceRedirectURI, interaction.Signal())
}

func loginCodexBrowser(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	flow, err := createCodexAuthorizationFlow("pi")
	if err != nil {
		return nil, err
	}
	server := startCodexLocalServer(flow.State)
	defer server.close()

	manualCtx, cancelManual := context.WithCancel(interaction.Signal())
	defer cancelManual()
	stopAbort := context.AfterFunc(interaction.Signal(), func() { server.cancelWait() })
	defer stopAbort()

	interaction.Notify(authtypes.AuthEvent{
		Type:         authtypes.AuthEventAuthURL,
		URL:          &flow.URL,
		Instructions: stringPtr("A browser window should open. Complete login to finish."),
	})

	type manualResult struct {
		input *string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	placeholder := codexRedirectURI
	go func() {
		input, promptErr := interaction.Prompt(manualCtx, authtypes.AuthPrompt{
			Type:        authtypes.AuthPromptManualCode,
			Message:     "Complete login in your browser, or paste the authorization code / redirect URL here:",
			Placeholder: &placeholder,
		})
		manualCh <- manualResult{input: &input, err: promptErr}
		server.cancelWait()
	}()

	var code string
	var manualInput *string
	var manualErr error
	result := server.waitForCode()
	if result != nil {
		code = *result
	} else {
		manual := <-manualCh
		manualInput = manual.input
		manualErr = manual.err
	}
	applyManual := func() {
		if manualInput == nil {
			return
		}
		parsedCode, parsedState := parseCodexAuthorizationInput(*manualInput)
		if parsedState != nil && *parsedState != flow.State {
			manualErr = errors.New("State mismatch")
			return
		}
		if parsedCode != nil {
			code = *parsedCode
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
	return exchangeCodexCodeForCredentials(ctx, code, flow.Verifier, codexRedirectURI, interaction.Signal())
}

func loginCodex(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	method, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
		Type:    authtypes.AuthPromptSelect,
		Message: "Select OpenAI Codex login method:",
		Options: []authtypes.AuthPromptOption{
			{ID: codexBrowserLoginMethod, Label: "Browser login (default)"},
			{ID: codexDeviceLoginMethod, Label: "Device code login (headless)"},
		},
	})
	if err != nil {
		return nil, err
	}
	switch method {
	case codexDeviceLoginMethod:
		return loginCodexDeviceCode(ctx, interaction)
	case codexBrowserLoginMethod:
		return loginCodexBrowser(ctx, interaction)
	default:
		return nil, fmt.Errorf("Unknown OpenAI Codex login method: %s", method)
	}
}

func refreshCodexToken(ctx context.Context, refreshToken string) (*authtypes.OAuthCredential, error) {
	token, err := refreshCodexAccessToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	return credentialsFromCodexToken(token)
}

// OpenAICodexOAuth is the OpenAI (ChatGPT Plus/Pro) OAuth flow.
//
// Ports `openaiCodexOAuth` from packages/ai/src/auth/oauth/openai-codex.ts.
var OpenAICodexOAuth = &authtypes.OAuthAuth{
	Name:           "OpenAI (ChatGPT Plus/Pro)",
	IsSubscription: true,
	Login:          loginCodex,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return refreshCodexToken(ctx, credential.Refresh)
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
	},
}
