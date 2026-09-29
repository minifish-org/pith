package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"context"

	"github.com/minifish-org/pith/packages/ai/types"
)

// The upstream catalog tests in testdata/reference-ledger.json are empty for
// this batch (the catalogs are generated data), so these self-tests check the
// observable contract of the Go port: every provider export is a faithful
// flattening of its embedded JSON document, the provider id is recorded on each
// model, and the Radius config helpers preserve their documented behavior.

func TestFlattenMatchesEmbeddedDocument(t *testing.T) {
	document := MustDocument("cerebras.json")
	groups, err := flattenForTest(document)
	if err != nil {
		t.Fatalf("flatten embedded document: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("expected at least one API group")
	}
	total := 0
	for _, entries := range groups {
		total += len(entries)
	}
	if total != len(CEREBRAS_MODELS) {
		t.Fatalf("flattened model count: want %d, got %d", total, len(CEREBRAS_MODELS))
	}
	for id, model := range CEREBRAS_MODELS {
		if model.Model.Id != id {
			t.Errorf("model %q has id %q", id, model.Model.Id)
		}
		if model.Model.Provider != types.ProviderCerebras {
			t.Errorf("model %q provider: want %q, got %q", id, types.ProviderCerebras, model.Model.Provider)
		}
		if len(model.Raw()) == 0 {
			t.Errorf("model %q lost its raw JSON", id)
		}
	}
}

// flattenForTest splits a document into its API groups.
func flattenForTest(document []byte) (map[string]map[string]json.RawMessage, error) {
	keys, err := topLevelKeys(document)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		entries := map[string]json.RawMessage{}
		if err := decodeObjectField(document, key, &entries); err != nil {
			return nil, err
		}
		out[key] = entries
	}
	return out, nil
}

func TestCatalogModelRoundTripsVerbatim(t *testing.T) {
	for id, model := range OPENAI_MODELS {
		raw := model.Raw()
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("model %q raw is not JSON: %v", id, err)
		}
		marshaled, err := json.Marshal(model)
		if err != nil {
			t.Fatalf("model %q marshal: %v", id, err)
		}
		if string(marshaled) != string(raw) {
			t.Errorf("model %q was rewritten: %s -> %s", id, raw, marshaled)
		}
	}
}

func TestMODELSKeysAreKnownProviders(t *testing.T) {
	for provider := range MODELS {
		if !provider.Known() {
			t.Errorf("MODELS contains unknown provider %q", provider)
		}
	}
	for _, provider := range types.KnownProviders() {
		if _, ok := MODELS[types.ProviderId(provider)]; !ok {
			t.Errorf("MODELS is missing known provider %q", provider)
		}
	}
}

func TestImageModelsExposeOpenRouter(t *testing.T) {
	group, ok := IMAGE_MODELS[types.ProviderOpenRouterImages]
	if !ok {
		t.Fatal("IMAGE_MODELS is missing the openrouter image provider")
	}
	if len(group) == 0 {
		t.Fatal("openrouter image catalog is empty")
	}
	for id, model := range group {
		if model.Id != id {
			t.Errorf("image model %q has id %q", id, model.Id)
		}
		if model.Api != types.ApiOpenRouterImages {
			t.Errorf("image model %q api: want %q, got %q", id, types.ApiOpenRouterImages, model.Api)
		}
		if model.BaseUrl != "https://openrouter.ai/api/v1" {
			t.Errorf("image model %q baseUrl: %q", id, model.BaseUrl)
		}
		if len(model.Output) == 0 {
			t.Errorf("image model %q has no output modalities", id)
		}
	}
}

func TestLoadRadiusGatewayConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/config" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("authorization") != "Bearer secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"baseUrl":"https://radius.example","models":[{"id":"m","name":"M","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":1000,"maxTokens":100}]}`))
	}))
	defer server.Close()

	config, err := LoadRadiusGatewayConfig(context.Background(), server.URL, "secret", server.Client())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.BaseUrl != "https://radius.example" {
		t.Errorf("baseUrl: %q", config.BaseUrl)
	}
	if len(config.Models) != 1 || config.Models[0].Id != "m" {
		t.Fatalf("models: %+v", config.Models)
	}

	models := GetRadiusModelsFromConfig("radius", config)
	if len(models) != 1 {
		t.Fatalf("expanded models: %d", len(models))
	}
	if models[0].Api != types.ApiPiMessages || models[0].Provider != types.ProviderRadius {
		t.Errorf("expanded model binding: %+v", models[0])
	}
	if models[0].BaseUrl != "https://radius.example" {
		t.Errorf("expanded model baseUrl: %q", models[0].BaseUrl)
	}
}

func TestLoadRadiusGatewayConfigRejectsInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"baseUrl":5}`))
	}))
	defer server.Close()

	if _, err := LoadRadiusGatewayConfig(context.Background(), server.URL, "", server.Client()); err == nil {
		t.Fatal("expected an error for an invalid config")
	}
}

func TestNormalizeRadiusGatewayUrl(t *testing.T) {
	cases := map[string]string{
		"radius.pi.dev":             "https://radius.pi.dev",
		"https://radius.pi.dev/":    "https://radius.pi.dev",
		"http://radius.pi.dev//":    "http://radius.pi.dev",
		"https://radius.pi.dev/a/b": "https://radius.pi.dev/a/b",
	}
	for input, want := range cases {
		if got := NormalizeRadiusGatewayUrl(input); got != want {
			t.Errorf("NormalizeRadiusGatewayUrl(%q): want %q, got %q", input, want, got)
		}
	}
}

func TestGetRadiusModelsWithoutCredential(t *testing.T) {
	if models := GetRadiusModels("radius", nil); len(models) != 0 {
		t.Fatalf("expected no models without a credential, got %d", len(models))
	}
	if config := GetRadiusCredentialConfig(nil); config != nil {
		t.Fatalf("expected no config without a credential, got %+v", config)
	}
}
