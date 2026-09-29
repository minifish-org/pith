package images

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

func TestGetImageModelCatalog(t *testing.T) {
	model := GetImageModel(types.ProviderOpenRouterImages, "google/gemini-2.5-flash-image")
	if model == nil {
		t.Fatal("expected a known OpenRouter image model")
	}
	if model.Id != "google/gemini-2.5-flash-image" || model.Provider != types.ProviderOpenRouterImages {
		t.Fatalf("unexpected model: %+v", model)
	}
	if !containsOutput(model.Output, types.ImagesModelOutputText) {
		t.Fatalf("expected text output: %+v", model.Output)
	}
	if unknown := GetImageModel(types.ProviderOpenRouterImages, "does/not-exist"); unknown != nil {
		t.Fatalf("unknown model must be nil, got %+v", unknown)
	}
}

func TestGetImageProvidersAndModels(t *testing.T) {
	providers := GetImageProviders()
	if len(providers) == 0 {
		t.Fatal("expected at least one image provider")
	}
	found := false
	for _, provider := range providers {
		if provider == types.ProviderOpenRouterImages {
			found = true
		}
	}
	if !found {
		t.Fatalf("openrouter provider missing: %v", providers)
	}
	models := GetImageModels(types.ProviderOpenRouterImages)
	if len(models) == 0 {
		t.Fatal("expected OpenRouter image models")
	}
	for i := 1; i < len(models); i++ {
		if models[i-1].Id > models[i].Id {
			t.Fatalf("models must be sorted by id: %q before %q", models[i-1].Id, models[i].Id)
		}
	}
	if empty := GetImageModels("unknown-provider"); len(empty) != 0 {
		t.Fatalf("unknown provider must yield no models, got %+v", empty)
	}
}

func TestImagesAPIRegistry(t *testing.T) {
	ClearImagesAPIProviders()
	if GetImagesAPIProvider(types.ApiOpenRouterImages) != nil {
		t.Fatal("registry must start empty after clear")
	}

	called := false
	RegisterImagesAPIProvider(ImagesAPIProvider{
		API: types.ApiOpenRouterImages,
		GenerateImages: func(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
			called = true
			return &types.AssistantImages{Api: types.ImagesApi(model.Api), Provider: types.ImagesProviderId(model.Provider), Model: model.Id, StopReason: types.ImagesStopReasonStop}, nil
		},
	})
	provider := GetImagesAPIProvider(types.ApiOpenRouterImages)
	if provider == nil {
		t.Fatal("registered provider missing")
	}

	matching := &types.ImagesModel{Model: types.Model{Id: "m", Api: types.ApiOpenRouterImages, Provider: types.ProviderOpenRouterImages}}
	if _, err := provider.GenerateImages(matching, &types.ImagesContext{}, nil); err != nil {
		t.Fatalf("matching api should succeed: %v", err)
	}
	if !called {
		t.Fatal("provider function was not invoked")
	}

	mismatched := &types.ImagesModel{Model: types.Model{Id: "m", Api: types.Api("other"), Provider: types.ProviderOpenRouterImages}}
	if _, err := provider.GenerateImages(mismatched, &types.ImagesContext{}, nil); err == nil || !strings.Contains(err.Error(), "Mismatched api") {
		t.Fatalf("mismatched api must fail, got %v", err)
	}
}

func TestGenerateImagesBuiltinOpenRouter(t *testing.T) {
	ClearImagesAPIProviders()
	RegisterBuiltInImagesAPIProviders()

	responseBody := `{"id":"gen-1","choices":[{"message":{"content":"ok","images":[{"image_url":"data:image/png;base64,QUJD"}]}}]}`
	var recordedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedPath = r.URL.RequestURI()
		io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(responseBody))
	}))
	defer server.Close()

	model := types.ImagesModel{
		Model: types.Model{
			Id:        "openrouter/test",
			Name:      "Test",
			Api:       types.ApiOpenRouterImages,
			Provider:  types.ProviderId("openrouter"),
			BaseUrl:   server.URL,
			Input:     []types.ModelInputModality{types.ModelInputText},
			MaxTokens: 1024,
		},
		Output: []types.ImagesModelOutputModality{types.ImagesModelOutputImage},
	}
	apiKey := "fixture-key"
	context := &types.ImagesContext{Input: []types.ContentBlock{types.TextBlock("draw")}}
	output, err := GenerateImages(&model, context, &types.ImagesOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if recordedPath != "/chat/completions" {
		t.Fatalf("path = %q", recordedPath)
	}
	if output.StopReason != types.ImagesStopReasonStop || len(output.Output) != 2 {
		t.Fatalf("output = %+v", output)
	}
}

func TestGenerateImagesUnregistered(t *testing.T) {
	ClearImagesAPIProviders()
	model := &types.ImagesModel{Model: types.Model{Id: "m", Api: types.ApiOpenRouterImages, Provider: types.ProviderOpenRouterImages}}
	if _, err := GenerateImages(model, &types.ImagesContext{}, nil); err == nil {
		t.Fatal("unregistered api must fail")
	}
	// Restore the builtin registration for any later test.
	RegisterBuiltInImagesAPIProviders()
}

func containsOutput(output []types.ImagesModelOutputModality, target types.ImagesModelOutputModality) bool {
	for _, modality := range output {
		if modality == target {
			return true
		}
	}
	return false
}
