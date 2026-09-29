// This file is a Go port of packages/ai/src/auth/oauth/openrouter.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	openRouterAuthorizeURL     = "https://openrouter.ai/auth"
	openRouterTokenURL         = "https://openrouter.ai/api/v1/auth/keys"
	openRouterLoginTimeout     = 5 * time.Minute
	openRouterTokenExchangeTMO = 30 * time.Second
)

// openRouterMaxSafeInteger mirrors Number.MAX_SAFE_INTEGER for permanent keys.
const openRouterMaxSafeInteger = float64(9007199254740991)

var openRouterHTTPClient = &http.Client{Timeout: 2 * time.Minute}

func openRouterCallbackHost() string {
	if value := utils.GetProviderEnvValue("PI_OAUTH_CALLBACK_HOST", nil); value != nil && *value != "" {
		return *value
	}
	return "127.0.0.1"
}

func randomUUID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return ""
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func parseOpenRouterAuthorizationInput(input string) *string {
	value := strings.TrimSpace(input)
	if value == "" {
		return nil
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		if code := parsed.Query().Get("code"); code != "" {
			return &code
		}
		return nil
	}
	if strings.Contains(value, "code=") {
		if params, err := url.ParseQuery(value); err == nil {
			if code := params.Get("code"); code != "" {
				return &code
			}
			return nil
		}
	}
	code := value
	return &code
}

func openRouterErrorDetail(body map[string]any) string {
	if value, ok := body["error_description"].(string); ok {
		return value
	}
	if value, ok := body["message"].(string); ok {
		return value
	}
	if value, ok := body["error"].(string); ok {
		return value
	}
	if object, ok := body["error"].(map[string]any); ok {
		if message, ok := object["message"].(string); ok {
			return message
		}
	}
	return ""
}

func exchangeOpenRouterCode(ctx context.Context, code, verifier string, signal context.Context) (*authtypes.OAuthCredential, error) {
	if signal != nil && signal.Err() != nil {
		return nil, errors.New("Login cancelled")
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, openRouterTokenExchangeTMO)
	defer cancel()
	stopAbort := context.AfterFunc(signal, cancel)
	defer stopAbort()

	payload, _ := json.Marshal(map[string]any{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	request, err := http.NewRequestWithContext(exchangeCtx, http.MethodPost, openRouterTokenURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/json")
	response, err := openRouterHTTPClient.Do(request)
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		if exchangeCtx.Err() != nil {
			return nil, errors.New("OpenRouter OAuth token exchange timed out")
		}
		return nil, err
	}
	defer response.Body.Close()
	bodyBytes, _ := io.ReadAll(response.Body)
	body := map[string]any{}
	if len(bytes.TrimSpace(bodyBytes)) > 0 {
		var parsed any
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil, errors.New("OpenRouter OAuth returned invalid JSON")
			}
		} else if object, ok := parsed.(map[string]any); ok {
			body = object
		}
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := openRouterErrorDetail(body)
		if detail != "" {
			return nil, fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d): %s", response.StatusCode, detail)
		}
		return nil, fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d)", response.StatusCode)
	}
	key, ok := body["key"].(string)
	if !ok || key == "" {
		return nil, errors.New(`OpenRouter OAuth response carries no "key"`)
	}
	return &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Access:  key,
			Refresh: "",
			Expires: openRouterMaxSafeInteger,
		},
		Type: authtypes.CredentialTypeOAuth,
	}, nil
}

type openRouterCallbackServer struct {
	callbackURL       string
	close             func()
	cancelWait        func()
	waitForCredential func() (*authtypes.OAuthCredential, error)
}

func startOpenRouterCallbackServer(callbackPath, verifier string, signal context.Context) (*openRouterCallbackServer, error) {
	if signal != nil && signal.Err() != nil {
		return nil, errors.New("Login cancelled")
	}
	callbackHost := openRouterCallbackHost()
	listener, err := net.Listen("tcp", callbackHost+":0")
	if err != nil {
		return nil, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("Could not determine the OpenRouter OAuth callback port")
	}

	type outcome struct {
		credential *authtypes.OAuthCredential
		err        error
	}
	outcomeCh := make(chan outcome, 1)
	settled := false
	claimed := false
	var timeout *time.Timer
	var stopAbort func() bool

	closeServer := func() {
		if timeout != nil {
			timeout.Stop()
		}
		if stopAbort != nil {
			stopAbort()
		}
		_ = listener.Close()
	}
	finish := func(result outcome) {
		if settled {
			return
		}
		settled = true
		closeServer()
		outcomeCh <- result
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL, parseErr := url.Parse(r.URL.String())
		if parseErr != nil || r.Method != http.MethodGet || requestURL.Path != callbackPath {
			sendOpenRouterHTML(w, http.StatusNotFound, OAuthErrorHTML("OAuth callback route not found.", nil))
			return
		}
		if claimed || settled {
			sendOpenRouterHTML(w, http.StatusConflict, OAuthErrorHTML("This OAuth callback has already been used.", nil))
			return
		}
		if oauthError := requestURL.Query().Get("error"); oauthError != "" {
			description := requestURL.Query().Get("error_description")
			if description == "" {
				description = oauthError
			}
			sendOpenRouterHTML(w, http.StatusBadRequest, OAuthErrorHTML("OpenRouter authorization was denied.", &description))
			finish(outcome{err: fmt.Errorf("OpenRouter authorization failed: %s", description)})
			return
		}
		code := requestURL.Query().Get("code")
		if code == "" {
			sendOpenRouterHTML(w, http.StatusBadRequest, OAuthErrorHTML("OpenRouter returned no authorization code.", nil))
			return
		}
		claimed = true
		credential, exchangeErr := exchangeOpenRouterCode(r.Context(), code, verifier, signal)
		if exchangeErr != nil {
			message := exchangeErr.Error()
			sendOpenRouterHTML(w, http.StatusBadGateway, OAuthErrorHTML("OpenRouter key exchange failed.", &message))
			finish(outcome{err: exchangeErr})
			return
		}
		sendOpenRouterHTML(w, http.StatusOK, OAuthSuccessHTML("Signed in to OpenRouter. You may now close this page."))
		finish(outcome{credential: credential})
	})

	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	stopAbort = context.AfterFunc(signal, func() { finish(outcome{err: errors.New("Login cancelled")}) })
	if signal != nil && signal.Err() != nil {
		closeServer()
		return nil, errors.New("Login cancelled")
	}
	timeout = time.AfterFunc(openRouterLoginTimeout, func() { finish(outcome{err: errors.New("OpenRouter OAuth login timed out")}) })

	return &openRouterCallbackServer{
		callbackURL: fmt.Sprintf("http://%s:%d%s", callbackHost, address.Port, callbackPath),
		close:       func() { _ = server.Close() },
		cancelWait: func() {
			if !claimed {
				finish(outcome{credential: nil})
			}
		},
		waitForCredential: func() (*authtypes.OAuthCredential, error) {
			result := <-outcomeCh
			return result.credential, result.err
		},
	}, nil
}

func sendOpenRouterHTML(w http.ResponseWriter, status int, html string) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, html)
}

func loginOpenRouter(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	callbackPath := "/oauth/callback/" + randomUUID()
	callback, err := startOpenRouterCallbackServer(callbackPath, pkce.Verifier, interaction.Signal())
	if err != nil {
		return nil, err
	}
	manualCtx, cancelManual := context.WithCancel(interaction.Signal())
	defer cancelManual()
	defer callback.close()

	authorize, err := url.Parse(openRouterAuthorizeURL)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("callback_url", callback.callbackURL)
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", "S256")
	authorize.RawQuery = params.Encode()

	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: stringPtr("Listening for OpenRouter OAuth callback on " + callback.callbackURL)})
	interaction.Notify(authtypes.AuthEvent{
		Type:         authtypes.AuthEventAuthURL,
		URL:          stringPtr(authorize.String()),
		Instructions: stringPtr("Complete sign-in in your browser. If the browser is on another machine, paste the final redirect URL here."),
	})

	type manualResult struct {
		input *string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	placeholder := callback.callbackURL
	go func() {
		input, promptErr := interaction.Prompt(manualCtx, authtypes.AuthPrompt{
			Type:        authtypes.AuthPromptManualCode,
			Message:     "Complete sign-in in your browser, or paste the authorization code / redirect URL here:",
			Placeholder: &placeholder,
		})
		manualCh <- manualResult{input: &input, err: promptErr}
		callback.cancelWait()
	}()

	credential, waitErr := callback.waitForCredential()
	if waitErr != nil {
		return nil, waitErr
	}
	if credential != nil {
		return credential, nil
	}

	manual := <-manualCh
	if manual.err != nil {
		return nil, manual.err
	}
	var code string
	if manual.input != nil {
		if parsed := parseOpenRouterAuthorizationInput(*manual.input); parsed != nil {
			code = *parsed
		}
	}
	if code == "" {
		return nil, errors.New("Missing authorization code")
	}
	interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: stringPtr("Exchanging authorization code for an API key...")})
	return exchangeOpenRouterCode(ctx, code, pkce.Verifier, interaction.Signal())
}

// OpenRouterOAuth is the OpenRouter OAuth PKCE flow.
//
// Ports `openRouterOAuth` from packages/ai/src/auth/oauth/openrouter.ts.
var OpenRouterOAuth = &authtypes.OAuthAuth{
	Name:       "OpenRouter OAuth",
	LoginLabel: "Sign in with OpenRouter",
	Login:      loginOpenRouter,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return credential, nil
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access)}, nil
	},
}
