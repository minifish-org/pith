package providers

import (
	"context"
	"testing"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// TestBuiltinCatalogReads pins the generated-catalog reads: a known model is
// resolvable, unknown ids are absent, and the manifest timestamp is present.
func TestBuiltinCatalogReads(t *testing.T) {
	generatedAt := GetBuiltinModelDataGeneratedAt()
	if generatedAt == nil || *generatedAt <= 0 {
		t.Fatalf("expected a generated-at timestamp, got %v", generatedAt)
	}
	model := GetBuiltinModel(types.ProviderDeepSeek, "deepseek-flash")
	if model == nil || model.Provider != types.ProviderDeepSeek {
		t.Fatalf("expected DeepSeek model, got %+v", model)
	}
	if unknown := GetBuiltinModel(types.ProviderDeepSeek, "does-not-exist"); unknown != nil {
		t.Fatalf("unknown model must be nil, got %+v", unknown)
	}
	if models := GetBuiltinModels(types.ProviderDeepSeek); len(models) == 0 {
		t.Fatal("expected DeepSeek catalog models")
	}
	if models := GetBuiltinModels("unknown-provider"); len(models) != 0 {
		t.Fatalf("unknown provider must yield no models, got %+v", models)
	}
	providers := GetBuiltinProviders()
	if len(providers) != len(catalog.MODELS) {
		t.Fatalf("builtin providers %d != catalog entries %d", len(providers), len(catalog.MODELS))
	}
}

// TestBuiltinProvidersHydrateCatalog verifies every constructed built-in
// provider reports its catalog models and a stable identity.
func TestBuiltinProvidersHydrateCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, provider := range BuiltinProviders() {
		if provider.ID() == "" || provider.Name() == "" {
			t.Fatalf("provider missing identity: %q/%q", provider.ID(), provider.Name())
		}
		if seen[provider.ID()] {
			t.Fatalf("duplicate provider id %q", provider.ID())
		}
		seen[provider.ID()] = true
		if provider.Auth().APIKey == nil && provider.Auth().OAuth == nil {
			t.Fatalf("provider %q has no auth", provider.ID())
		}
		models := provider.GetModels()
		if provider.ID() == string(types.ProviderRadius) {
			if len(models) == 0 {
				t.Fatal("radius must expose its baseline catalog")
			}
			continue
		}
		expected := GetBuiltinModels(types.KnownProvider(provider.ID()))
		if len(models) != len(expected) {
			t.Fatalf("provider %q models %d != catalog %d", provider.ID(), len(models), len(expected))
		}
	}
}

// TestBuiltinModelsRegistersProviders checks the collection installs every
// built-in provider.
func TestBuiltinModelsRegistersProviders(t *testing.T) {
	models := BuiltinModels(nil)
	if len(models.GetProviders()) != len(BuiltinProviders()) {
		t.Fatalf("registered providers %d != %d", len(models.GetProviders()), len(BuiltinProviders()))
	}
}

// TestFauxProviderScriptedResponses exercises the scripted faux provider end to
// end without network access.
func TestFauxProviderScriptedResponses(t *testing.T) {
	handle := FauxProvider()
	model := handle.GetModel()
	if model == nil {
		t.Fatal("expected a default faux model")
	}
	if handle.API != FauxDefaultAPI {
		t.Fatalf("default api = %q, want %q", handle.API, FauxDefaultAPI)
	}
	handle.SetResponses([]FauxResponseStep{{Message: messagePtr(FauxAssistantMessage("hello"))}})
	stream := handle.Provider.Stream(*model, types.NewTranscriptContext(nil), &types.StreamOptions{})
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "hello" {
		t.Fatalf("unexpected content: %+v", result.Content)
	}
	if handle.State.CallCount != 1 {
		t.Fatalf("call count = %d", handle.State.CallCount)
	}
}

func messagePtr(message types.AssistantMessage) *types.AssistantMessage {
	return &message
}

// TestRadiusProviderBaseline pins the Radius gateway baseline catalog.
func TestRadiusProviderBaseline(t *testing.T) {
	provider := RadiusProvider(RadiusProviderOptions{})
	if provider.ID() != "radius" {
		t.Fatalf("radius id = %q", provider.ID())
	}
	if len(provider.GetModels()) == 0 {
		t.Fatal("expected baseline radius models")
	}
	custom := RadiusProvider(RadiusProviderOptions{ID: "radius-custom", Name: "Custom", Gateway: "https://example.test"})
	if custom.ID() != "radius-custom" || custom.Name() != "Custom" {
		t.Fatalf("unexpected custom radius identity: %q/%q", custom.ID(), custom.Name())
	}
	if len(custom.GetModels()) != 0 {
		t.Fatalf("custom gateway must start with an empty baseline, got %d", len(custom.GetModels()))
	}
}

// TestOpenCodeSessionHeaderInjection checks the OpenCode routing header
// injection does not mutate the caller's options.
func TestOpenCodeSessionHeaderInjection(t *testing.T) {
	sessionID := "session-1"
	original := &types.StreamOptions{SessionId: &sessionID}
	injected := withOpenCodeSessionHeader(original)
	if injected == original {
		t.Fatal("expected a cloned options value")
	}
	if value := injected.Headers[openCodeSessionHeader]; value == nil || *value != sessionID {
		t.Fatalf("missing session header: %+v", injected.Headers)
	}
	if original.Headers != nil {
		t.Fatalf("original options must not be mutated: %+v", original.Headers)
	}
	present := types.ProviderHeaders{"X-OpenCode-Session": strPtr("explicit")}
	withHeader := &types.StreamOptions{
		SessionId:              &sessionID,
		ProviderRequestOptions: types.ProviderRequestOptions{Headers: present},
	}
	if got := withOpenCodeSessionHeader(withHeader); got != withHeader {
		t.Fatal("an explicit header must win and return the same options")
	}
}

// TestCloudflareModelPlaceholderResolution checks account/gateway endpoint
// placeholders materialize from the provider env.
func TestCloudflareModelPlaceholderResolution(t *testing.T) {
	account := "acct"
	gateway := "gw"
	model := types.Model{BaseUrl: "https://api.cloudflare.com/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}"}
	resolved := ResolveCloudflareModel(model, types.ProviderEnv{
		"CLOUDFLARE_ACCOUNT_ID": &account,
		"CLOUDFLARE_GATEWAY_ID": &gateway,
	})
	if resolved.BaseUrl != "https://api.cloudflare.com/acct/gw" {
		t.Fatalf("unexpected resolved base url: %q", resolved.BaseUrl)
	}
	if same := ResolveCloudflareModel(model, nil); same.BaseUrl != model.BaseUrl {
		t.Fatalf("nil env must leave the url unchanged, got %q", same.BaseUrl)
	}
}

func strPtr(value string) *string { return &value }

var _ ai.Provider = RadiusProvider(RadiusProviderOptions{})
