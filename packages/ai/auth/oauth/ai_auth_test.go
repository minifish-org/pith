package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func neverAborted() context.Context { return context.Background() }

// PKCE verifiers are 43-character url-safe strings whose S256 challenge matches
// and which differ across generations.
func TestGeneratePKCE(t *testing.T) {
	first, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Verifier) != 43 {
		t.Fatalf("verifier length = %d; want 43", len(first.Verifier))
	}
	if strings.Trim(first.Verifier, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		t.Fatalf("verifier not url-safe: %q", first.Verifier)
	}
	sum := sha256.Sum256([]byte(first.Verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != first.Challenge {
		t.Fatal("challenge does not match verifier")
	}
	if first.Verifier == second.Verifier {
		t.Fatal("two verifiers were identical")
	}
}

// The OAuth pages escape user-controlled message and detail text.
func TestOAuthPagesEscapeHTML(t *testing.T) {
	success := OAuthSuccessHTML("<ok> & \"done\"")
	if !strings.Contains(success, "Authentication successful") {
		t.Fatal("success page missing heading")
	}
	if strings.Contains(success, "<ok>") || !strings.Contains(success, "&lt;ok&gt;") {
		t.Fatalf("success page did not escape message: %s", success)
	}
	details := "<script>"
	errorPage := OAuthErrorHTML("failed", &details)
	if !strings.Contains(errorPage, "Authentication failed") || !strings.Contains(errorPage, "&lt;script&gt;") {
		t.Fatalf("error page did not escape details: %s", errorPage)
	}
}

// The poll loop returns a completed value without waiting.
func TestPollOAuthDeviceCodeImmediateComplete(t *testing.T) {
	interval := float64(1)
	expires := float64(30)
	value, err := PollOAuthDeviceCodeFlow(context.Background(), OAuthDeviceCodePollOptions[string]{
		IntervalSeconds:  &interval,
		ExpiresInSeconds: &expires,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			return DeviceCodeComplete("token"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value != "token" {
		t.Fatalf("value = %q", value)
	}
}

// A failed poll surfaces its message.
func TestPollOAuthDeviceCodeFailed(t *testing.T) {
	interval := float64(1)
	expires := float64(30)
	_, err := PollOAuthDeviceCodeFlow(context.Background(), OAuthDeviceCodePollOptions[string]{
		IntervalSeconds:  &interval,
		ExpiresInSeconds: &expires,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			return DeviceCodeFailed[string]("denied"), nil
		},
	})
	if err == nil || err.Error() != "denied" {
		t.Fatalf("error = %v", err)
	}
}

// Cancelling an in-flight wait rejects with the cancel message.
func TestPollOAuthDeviceCodeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	interval := float64(1)
	expires := float64(30)
	done := make(chan error, 1)
	go func() {
		_, err := PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[string]{
			IntervalSeconds:  &interval,
			ExpiresInSeconds: &expires,
			Poll: func() (OAuthDeviceCodePollResult[string], error) {
				return DeviceCodePending[string](), nil
			},
		})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "Login cancelled" {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not cancel")
	}
}

// Anthropic login resolves through the manual-code prompt, exchanges the code
// and derives the api key from the access token.
func TestAnthropicOAuthManualLoginAndRefresh(t *testing.T) {
	originalClient := anthropicHTTPClient
	originalServer := startAnthropicServer
	defer func() {
		anthropicHTTPClient = originalClient
		startAnthropicServer = originalServer
	}()

	var mu sync.Mutex
	var bodies []map[string]any
	anthropicHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(request.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		return jsonHTTPResponse(200, `{"access_token":"access-token","refresh_token":"refresh-token","expires_in":3600}`), nil
	})}
	startAnthropicServer = func(ctx context.Context, expectedState string) (*anthropicCallbackServer, error) {
		return &anthropicCallbackServer{
			server:      &http.Server{},
			redirectURI: anthropicRedirectURI,
			cancelWait:  func() {},
			waitForCode: func() *anthropicAuthCode { return nil },
		}, nil
	}

	var notified []authtypes.AuthEvent
	interaction := authtypes.NewInteraction(neverAborted(), func(ctx context.Context, prompt authtypes.AuthPrompt) (string, error) {
		if prompt.Type != authtypes.AuthPromptManualCode {
			t.Fatalf("unexpected prompt %q", prompt.Type)
		}
		return "manual-code", nil
	}, func(event authtypes.AuthEvent) { notified = append(notified, event) })

	credential, err := AnthropicOAuth.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "access-token" || credential.Refresh != "refresh-token" {
		t.Fatalf("credential = %#v", credential)
	}
	if len(notified) == 0 || notified[0].Type != authtypes.AuthEventAuthURL {
		t.Fatalf("notifications = %#v", notified)
	}
	if len(bodies) != 1 || bodies[0]["grant_type"] != "authorization_code" || bodies[0]["redirect_uri"] != anthropicRedirectURI {
		t.Fatalf("exchange body = %#v", bodies)
	}

	refreshed, err := AnthropicOAuth.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Access != "access-token" {
		t.Fatalf("refreshed = %#v", refreshed)
	}
	if _, present := bodies[len(bodies)-1]["scope"]; present {
		t.Fatalf("refresh request must omit scope: %#v", bodies[len(bodies)-1])
	}
}

// Each subscription provider reports its subscription flag and derives request
// auth from the stored credential.
func TestProviderSubscriptionFlagsAndToAuth(t *testing.T) {
	subscriptions := []*authtypes.OAuthAuth{AnthropicOAuth, OpenAICodexOAuth, GitHubCopilotOAuth, KimiCodingOAuth, XaiOAuth}
	for _, flow := range subscriptions {
		if !flow.IsSubscription {
			t.Fatalf("%s should be a subscription flow", flow.Name)
		}
	}
	if OpenRouterOAuth.IsSubscription {
		t.Fatal("OpenRouter must not be a subscription flow")
	}

	credential := authtypes.NewOAuthCredential("r", "token", 0)
	apiKeyOnly := []*authtypes.OAuthAuth{AnthropicOAuth, OpenAICodexOAuth, OpenRouterOAuth, XaiOAuth, MetaOAuth}
	for _, flow := range apiKeyOnly {
		result, err := flow.ToAuth(context.Background(), credential)
		if err != nil {
			t.Fatalf("%s toAuth: %v", flow.Name, err)
		}
		if result.APIKey == nil || *result.APIKey != "token" {
			t.Fatalf("%s toAuth = %#v", flow.Name, result)
		}
	}
	if kimiResult, err := KimiCodingOAuth.ToAuth(context.Background(), credential); err != nil {
		t.Fatal(err)
	} else if kimiResult.Headers["Authorization"] == nil || *kimiResult.Headers["Authorization"] != "Bearer token" {
		t.Fatalf("kimi toAuth headers = %#v", kimiResult.Headers)
	}
}

// GitHub Copilot derives the proxy baseUrl from proxy-ep, then the enterprise
// domain, then the individual endpoint.
func TestGitHubCopilotBaseURL(t *testing.T) {
	access := "tid=abc;exp=123;proxy-ep=proxy.enterprise.example;rest"
	result, err := GitHubCopilotOAuth.ToAuth(context.Background(), authtypes.NewOAuthCredential("r", access, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseURL == nil || *result.BaseURL != "https://api.enterprise.example" {
		t.Fatalf("proxy-ep baseUrl = %#v", result.BaseURL)
	}

	enterprise := authtypes.NewOAuthCredential("r", "no-proxy-ep", 0)
	enterprise.SetExtra("enterpriseUrl", "https://company.ghe.com")
	result, err = GitHubCopilotOAuth.ToAuth(context.Background(), enterprise)
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseURL == nil || *result.BaseURL != "https://copilot-api.company.ghe.com" {
		t.Fatalf("enterprise baseUrl = %#v", result.BaseURL)
	}

	individual := authtypes.NewOAuthCredential("r", "no-proxy-ep", 0)
	result, err = GitHubCopilotOAuth.ToAuth(context.Background(), individual)
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseURL == nil || *result.BaseURL != "https://api.individual.githubcopilot.com" {
		t.Fatalf("individual baseUrl = %#v", result.BaseURL)
	}
}

// Kimi Code refresh reparses the token response and preserves the bearer
// toAuth.
func TestKimiCodingRefresh(t *testing.T) {
	originalClient := kimiHTTPClient
	defer func() { kimiHTTPClient = originalClient }()
	kimiHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(200, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`), nil
	})}
	t.Setenv("KIMI_CODE_OAUTH_HOST", "https://kimi.example")

	refreshed, err := KimiCodingOAuth.Refresh(context.Background(), authtypes.NewOAuthCredential("old-refresh", "old-access", 0))
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Access != "new-access" || refreshed.Refresh != "new-refresh" || refreshed.Expires <= float64(time.Now().UnixMilli()) {
		t.Fatalf("refreshed = %#v", refreshed)
	}
}

// xAI refresh retains the previous refresh token when the server omits it.
func TestXaiRefreshRetainsRefreshToken(t *testing.T) {
	originalClient := xaiHTTPClient
	defer func() { xaiHTTPClient = originalClient }()
	xaiHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(200, `{"access_token":"new-access","expires_in":3600}`), nil
	})}

	refreshed, err := XaiOAuth.Refresh(context.Background(), authtypes.NewOAuthCredential("kept-refresh", "old-access", 0))
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Access != "new-access" || refreshed.Refresh != "kept-refresh" {
		t.Fatalf("refreshed = %#v", refreshed)
	}
}

// Meta re-mints the Model API key from the stored identity token on refresh.
func TestMetaRefreshMintsAPIKey(t *testing.T) {
	originalClient := metaHTTPClient
	defer func() { metaHTTPClient = originalClient }()
	metaHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonHTTPResponse(200, `{"api_key":"minted-key"}`), nil
	})}

	refreshed, err := MetaOAuth.Refresh(context.Background(), authtypes.NewOAuthCredential("identity-token", "old-key", 0))
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Access != "minted-key" || refreshed.Refresh != "identity-token" {
		t.Fatalf("refreshed = %#v", refreshed)
	}
}

// OpenRouter keeps the permanent credential on refresh.
func TestOpenRouterRefreshKeepsCredential(t *testing.T) {
	credential := authtypes.NewOAuthCredential("", "permanent-key", openRouterMaxSafeInteger)
	refreshed, err := OpenRouterOAuth.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed != credential {
		t.Fatal("OpenRouter refresh should return the same permanent credential")
	}
}

// Radius gateway flows expose the shared name and derive the api key.
func TestCreateRadiusOAuth(t *testing.T) {
	flow := CreateRadiusOAuth(RadiusOAuthOptions{Name: "Radius", Gateway: "https://radius.example/"})
	if flow.Name != "Radius" {
		t.Fatalf("name = %q", flow.Name)
	}
	result, err := flow.ToAuth(context.Background(), authtypes.NewOAuthCredential("r", "radius-token", 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.APIKey == nil || *result.APIKey != "radius-token" {
		t.Fatalf("radius toAuth = %#v", result)
	}
}

// Loaders return the concrete bundled flows.
func TestBundledOAuthLoaders(t *testing.T) {
	anthropic, err := LoadAnthropicOAuth()
	if err != nil || anthropic != AnthropicOAuth {
		t.Fatalf("anthropic loader = %#v, %v", anthropic, err)
	}
	radius, err := LoadRadiusOAuth(RadiusOAuthOptions{Name: "Custom", Gateway: "https://radius.example"})
	if err != nil || radius.Name != "Custom" {
		t.Fatalf("radius loader = %#v, %v", radius, err)
	}
}
