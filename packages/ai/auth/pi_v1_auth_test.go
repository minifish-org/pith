// This file holds the Pi 1.0 auth step self-tests. They are offline and
// source-derived: fake literal tokens, loopback 127.0.0.1, and no external
// network. The independent judge remains the authority for the PKCE/state/
// issuer boundary.
package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// ParseCallback accepts one valid code and rejects every unsafe shape.
func TestPiV1ParseCallbackBoundary(t *testing.T) {
	valid := "http://127.0.0.1:9876/callback?code=ok&state=nonce&iss=" + url.QueryEscape("https://issuer.example")
	if code, err := oauth.ParseCallback(valid, "nonce", "https://issuer.example"); err != nil || code != "ok" {
		t.Fatalf("valid callback = %q, %v", code, err)
	}
	// A bare code with no expected state/issuer is accepted.
	if code, err := oauth.ParseCallback("http://127.0.0.1/callback?code=only", "", ""); err != nil || code != "only" {
		t.Fatalf("bare callback = %q, %v", code, err)
	}
	// A fragmented copy-code response is accepted.
	if code, err := oauth.ParseCallback("https://client/callback#code=frag&state=s", "s", ""); err != nil || code != "frag" {
		t.Fatalf("fragment callback = %q, %v", code, err)
	}
	for _, bad := range []string{
		strings.Replace(valid, "state=nonce", "state=wrong", 1),
		strings.Replace(valid, "issuer.example", "evil.example", 1),
		valid + "&code=other",
		valid + "&state=other",
		"http://127.0.0.1/callback?state=nonce",
		"http://127.0.0.1/callback?state=nonce&error=access_denied",
	} {
		if _, err := oauth.ParseCallback(bad, "nonce", "https://issuer.example"); err == nil {
			t.Errorf("unsafe callback accepted: %q", bad)
		}
	}
}

// GeneratePKCE returns url-safe verifiers whose S256 challenge matches.
func TestPiV1GeneratePKCEShape(t *testing.T) {
	first, err := oauth.GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := first.Verifier, first.Challenge
	if len(verifier) != 43 || strings.ContainsAny(verifier, "+/=") {
		t.Fatalf("verifier = %q", verifier)
	}
	if challenge == "" || challenge == verifier {
		t.Fatalf("challenge = %q", challenge)
	}
}

// The shared callback server completes exactly once, then rejects a stale
// callback, and reports a cancellation without a value.
func TestPiV1CallbackServer(t *testing.T) {
	state := "expected"
	server, err := oauth.StartOAuthCallbackServer[string](oauth.OAuthCallbackServerOptions[string]{
		ProviderName: "Example",
		Host:         "127.0.0.1",
		Port:         0,
		Path:         "/callback",
		State:        &state,
		Complete: func(_ context.Context, code string) (string, error) {
			return "completed:" + code, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if !strings.HasPrefix(server.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("redirect uri = %q", server.RedirectURI)
	}
	response, err := http.Get(server.RedirectURI + "?code=the-code&state=expected")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Signed in to Example.") {
		t.Fatalf("success = %d %q", response.StatusCode, body)
	}
	value, err := server.Wait()
	if err != nil || value == nil || *value != "completed:the-code" {
		t.Fatalf("wait = %v, %v", value, err)
	}

	staleState := "expected"
	stale, err := oauth.StartOAuthCallbackServer[string](oauth.OAuthCallbackServerOptions[string]{
		ProviderName: "Example",
		Host:         "127.0.0.1",
		Port:         0,
		Path:         "/callback",
		State:        &staleState,
		Complete:     func(_ context.Context, code string) (string, error) { return code, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	stale.Cancel()
	if value, err := stale.Wait(); err != nil || value != nil {
		t.Fatalf("cancelled wait = %v, %v", value, err)
	}
	late, err := http.Get(stale.RedirectURI + "?code=late&state=expected")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(late.Body)
	late.Body.Close()
	if late.StatusCode != http.StatusConflict {
		t.Fatalf("stale callback status = %d", late.StatusCode)
	}
}

// WaitForCallbackOrManualInput falls back to manual input without a server and
// propagates prompt failures.
func TestPiV1WaitForCallbackOrManualInput(t *testing.T) {
	manual := authtypes.NewInteraction(neverAbortedContext(), func(_ context.Context, _ authtypes.AuthPrompt) (string, error) {
		return "pasted", nil
	}, nil)
	result, err := oauth.WaitForCallbackOrManualInput[string](context.Background(), manual, nil, oauth.OAuthManualPrompt{Message: "paste"})
	if err != nil || result.Type != oauth.OAuthResultManual || result.Input != "pasted" {
		t.Fatalf("manual result = %#v, %v", result, err)
	}

	failing := authtypes.NewInteraction(neverAbortedContext(), func(_ context.Context, _ authtypes.AuthPrompt) (string, error) {
		return "", context.Canceled
	}, nil)
	if _, err := oauth.WaitForCallbackOrManualInput[string](context.Background(), failing, nil, oauth.OAuthManualPrompt{Message: "paste"}); err == nil {
		t.Fatal("prompt failure was not propagated")
	}
}

// The OpenAI ChatGPT flow is a subscription flow, derives its api key from the
// access token, and validates the issued client ID before refreshing.
func TestPiV1OpenAIChatGPTContract(t *testing.T) {
	if !oauth.OpenAIChatGPTOAuth.IsSubscription || oauth.OpenAIChatGPTOAuth.LoginLabel != "Sign in with ChatGPT" {
		t.Fatalf("metadata = %#v", oauth.OpenAIChatGPTOAuth)
	}
	result, err := oauth.OpenAIChatGPTOAuth.ToAuth(context.Background(), authtypes.NewOAuthCredential("r", "access-token", 0))
	if err != nil || result.APIKey == nil || *result.APIKey != "access-token" {
		t.Fatalf("toAuth = %#v, %v", result, err)
	}
	// No client ID means reconnect; no token request should be attempted.
	if _, err := oauth.OpenAIChatGPTOAuth.Refresh(context.Background(), authtypes.NewOAuthCredential("r", "a", 0)); err == nil || !strings.Contains(err.Error(), "issued client ID") {
		t.Fatalf("refresh without clientId = %v", err)
	}
	// A missing or malformed device ID is rejected before authorization starts.
	interaction := authtypes.NewInteraction(neverAbortedContext(), func(_ context.Context, _ authtypes.AuthPrompt) (string, error) {
		return "", nil
	}, nil)
	if _, err := oauth.OpenAIChatGPTOAuth.Login(context.Background(), interaction); err == nil || !strings.Contains(err.Error(), "requires a device ID") {
		t.Fatalf("login without device id = %v", err)
	}
	if _, err := oauth.OpenAIChatGPTOAuth.LoginWithOptions(context.Background(), interaction, &authtypes.LoginOptions{GetDeviceID: func() string { return "not-a-uuid" }}); err == nil || !strings.Contains(err.Error(), "requires a device ID") {
		t.Fatalf("login with invalid device id = %v", err)
	}
}

// The Anthropic options-aware login offers browser/copy-code and rejects an
// unknown method; copy-code rejects missing input before any network call.
func TestPiV1AnthropicLoginMethods(t *testing.T) {
	unknown := authtypes.NewInteraction(neverAbortedContext(), func(_ context.Context, prompt authtypes.AuthPrompt) (string, error) {
		if prompt.Type != authtypes.AuthPromptSelect {
			t.Fatalf("unexpected prompt %q", prompt.Type)
		}
		return "bogus", nil
	}, nil)
	if _, err := oauth.AnthropicOAuth.LoginWithOptions(context.Background(), unknown, nil); err == nil || !strings.Contains(err.Error(), "Unknown Anthropic login method") {
		t.Fatalf("unknown method = %v", err)
	}

	var notified []authtypes.AuthEvent
	copyCode := authtypes.NewInteraction(neverAbortedContext(), func(_ context.Context, prompt authtypes.AuthPrompt) (string, error) {
		switch prompt.Type {
		case authtypes.AuthPromptSelect:
			return "copy_code", nil
		case authtypes.AuthPromptManualCode:
			return "", nil
		default:
			t.Fatalf("unexpected prompt %q", prompt.Type)
			return "", nil
		}
	}, func(event authtypes.AuthEvent) { notified = append(notified, event) })
	if _, err := oauth.AnthropicOAuth.LoginWithOptions(context.Background(), copyCode, nil); err == nil || !strings.Contains(err.Error(), "Missing authorization code") {
		t.Fatalf("copy-code missing input = %v", err)
	}
	if len(notified) == 0 || notified[0].Type != authtypes.AuthEventAuthURL {
		t.Fatalf("copy-code notifications = %#v", notified)
	}
	if _, err := oauth.LoadOpenAIChatGPTOAuth(); err != nil {
		t.Fatalf("chatgpt loader = %v", err)
	}
}

// The credential store round-trips both credential shapes.
func TestPiV1CredentialStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCredentialStore()
	key := "literal-api-key"
	if _, err := store.Modify(ctx, "p", func(authtypes.Credential) (authtypes.Credential, error) {
		return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &key}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	apiKey, ok := read.(*authtypes.ApiKeyCredential)
	if !ok || apiKey.Key == nil || *apiKey.Key != key {
		t.Fatalf("read = %#v", read)
	}
	if _, err := store.Modify(ctx, "p", func(authtypes.Credential) (authtypes.Credential, error) {
		return authtypes.NewOAuthCredential("literal-refresh", "literal-access", 1234), nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	read, err = store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	oauthCredential, ok := read.(*authtypes.OAuthCredential)
	if !ok || oauthCredential.Access != "literal-access" || oauthCredential.Refresh != "literal-refresh" {
		t.Fatalf("read = %#v", read)
	}
	if err := store.Delete(ctx, "p", nil); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Read(ctx, "p", nil)
	if err != nil || deleted != nil {
		t.Fatalf("deleted = %#v, %v", deleted, err)
	}
}

func neverAbortedContext() context.Context { return context.Background() }
