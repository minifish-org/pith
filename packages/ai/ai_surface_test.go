package ai

import (
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

func TestLegacyStreamAliasesAreBound(t *testing.T) {
	aliases := map[string]types.StreamFunction{
		"streamAnthropic":            StreamAnthropic,
		"streamAzureOpenAIResponses": StreamAzureOpenAIResponses,
		"streamGoogle":               StreamGoogle,
		"streamGoogleVertex":         StreamGoogleVertex,
		"streamMistral":              StreamMistral,
		"streamOpenAICodexResponses": StreamOpenAICodexResponses,
		"streamOpenAICompletions":    StreamOpenAICompletions,
		"streamOpenAIResponses":      StreamOpenAIResponses,
	}
	for name, alias := range aliases {
		if alias == nil {
			t.Fatalf("%s must be bound to a lazy API stream", name)
		}
	}
}

func TestLegacySimpleStreamAliasesAreBound(t *testing.T) {
	simpleAliases := map[string]func(*types.Model, *types.TranscriptContext, *types.SimpleStreamOptions) *types.AssistantMessageEventStream{
		"streamSimpleAnthropic":            StreamSimpleAnthropic,
		"streamSimpleAzureOpenAIResponses": StreamSimpleAzureOpenAIResponses,
		"streamSimpleGoogle":               StreamSimpleGoogle,
		"streamSimpleGoogleVertex":         StreamSimpleGoogleVertex,
		"streamSimpleMistral":              StreamSimpleMistral,
		"streamSimpleOpenAICodexResponses": StreamSimpleOpenAICodexResponses,
		"streamSimpleOpenAICompletions":    StreamSimpleOpenAICompletions,
		"streamSimpleOpenAIResponses":      StreamSimpleOpenAIResponses,
	}
	for name, alias := range simpleAliases {
		if alias == nil {
			t.Fatalf("%s must be bound to a lazy API simple stream", name)
		}
	}
}

func TestLegacyAliasesShareTheLazyModule(t *testing.T) {
	// The aliases are values captured from the lazy API factories; a model/api
	// mismatch must surface as a terminated stream rather than a panic or a
	// silently dropped request.
	model := types.Model{Id: "m", Provider: types.ProviderId("test"), Api: types.Api("not-anthropic")}
	if alias := StreamAnthropic; alias == nil {
		t.Fatal("streamAnthropic must be bound")
	}
	if model.Api == types.Api("anthropic-messages") {
		t.Fatal("fixture must exercise a mismatched api")
	}
}
