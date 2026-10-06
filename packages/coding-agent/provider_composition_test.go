package codingagent_test

import (
	"github.com/minifish-org/pith/packages/ai/api"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	codingagent "github.com/minifish-org/pith/packages/coding-agent"
	"testing"
)

func TestRegisteredCompatibleProviderRetainsComposedModelCatalog(t *testing.T) {
	runtime, err := codingagent.CreateModelRuntime(codingagent.CreateModelRuntimeOptions{AuthPath: t.TempDir() + "/auth.json"})
	if err != nil {
		t.Fatal(err)
	}
	contextWindow, maxTokens := float64(64000), float64(4096)
	for _, id := range []string{"custom-one", "custom-two"} {
		err = runtime.RegisterProvider(id, codingagent.ProviderConfigInput{
			Name: id, API: aitypes.ApiOpenAICompletions, BaseURL: "https://" + id + ".example/v1", APIKey: "fixture",
			StreamSimple: api.OpenAICompletionsApi().StreamSimple,
			Models:       []codingagent.ModelsJsonModel{{ID: "same-id", ContextWindow: &contextWindow, MaxTokens: &maxTokens}},
		})
		if err != nil {
			t.Fatal(err)
		}
		model := runtime.GetModel(id, "same-id")
		if model == nil || string(model.Provider) != id || model.ContextWindow != 64000 || model.Api != aitypes.ApiOpenAICompletions {
			t.Fatalf("registered model missing or metadata lost: %+v", model)
		}
	}
	if runtime.GetModel("openai", "gpt-5.5") == nil {
		t.Fatal("custom registration changed the built-in catalog")
	}
}
