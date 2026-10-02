package types_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// TestPiV1FoundationModelKinds covers the runtime model-type helpers, including
// the absent-type chat default, explicit chat/image/classifier tags, an unknown
// tag that must not become chat, nil, legacy DTOs and raw JSON.
func TestPiV1FoundationModelKinds(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent type is chat", `{"id":"same","provider":"p","api":"openai-completions"}`, "chat"},
		{"explicit chat", `{"type":"chat","id":"same","provider":"p"}`, "chat"},
		{"explicit image", `{"type":"image","id":"same","provider":"p"}`, "image"},
		{"explicit classifier", `{"type":"classifier","id":"same","provider":"p"}`, "classifier"},
		{"unknown tag is preserved", `{"type":"future","id":"same","provider":"p"}`, "future"},
		{"empty tag is chat", `{"type":"","id":"same","provider":"p"}`, "chat"},
		{"null tag is chat", `{"type":null,"id":"same","provider":"p"}`, "chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.raw)
			if got := types.GetModelType(raw); got != tc.want {
				t.Fatalf("raw type: got %q want %q", got, tc.want)
			}
			if got := types.GetModelType([]byte(tc.raw)); got != tc.want {
				t.Fatalf("bytes type: got %q want %q", got, tc.want)
			}
			for _, kind := range []string{"chat", "image", "classifier"} {
				want := tc.want == kind
				if types.IsModelType(raw, kind) != want {
					t.Fatalf("IsModelType(%s, %q) = %v, want %v", tc.raw, kind, !want, want)
				}
			}
		})
	}

	legacy := types.Model{Id: "old", Provider: "p"}
	if got := types.GetModelType(legacy); got != "chat" {
		t.Fatalf("legacy Model: got %q", got)
	}
	if got := types.GetModelType(&legacy); got != "chat" {
		t.Fatalf("legacy *Model: got %q", got)
	}
	if types.GetModelType(nil) != "" {
		t.Fatal("nil must have no model type")
	}
	if types.IsModelType(nil, "chat") {
		t.Fatal("nil must not be a chat model")
	}
	var nilModel *types.Model
	if types.GetModelType(nilModel) != "" {
		t.Fatal("typed nil *Model must have no model type")
	}
	image := types.ImagesModel{Model: legacy}
	if types.GetModelType(image) != "image" || types.GetModelType(&image) != "image" {
		t.Fatal("ImagesModel must be an image model")
	}
	classifier := types.NewClassifierModel("c", "C", "typesafe-system-one", "p", "https://x")
	if types.GetModelType(classifier) != "classifier" || types.GetModelType(&classifier) != "classifier" {
		t.Fatal("ClassifierModel must be a classifier model")
	}

	// Same id across kinds must not collide.
	chat := json.RawMessage(`{"type":"chat","id":"same","provider":"p","api":"openai-completions"}`)
	img := json.RawMessage(`{"type":"image","id":"same","provider":"p","api":"openrouter-images"}`)
	if !types.IsModelType(chat, "chat") || types.IsModelType(chat, "image") {
		t.Fatal("chat model narrowed incorrectly")
	}
	if !types.IsModelType(img, "image") || types.IsModelType(img, "chat") {
		t.Fatal("image model narrowed incorrectly")
	}
}

// TestPiV1FoundationAnyModelRoundTrip checks the tagged union keeps each kind's
// operation-specific fields and the verbatim object (so unknown kinds survive).
func TestPiV1FoundationAnyModelRoundTrip(t *testing.T) {
	imageJSON := `{"type":"image","id":"img","name":"Img","api":"openrouter-images","provider":"openrouter","baseUrl":"https://openrouter.ai/api/v1","input":["text"],"cost":{},"output":["image"]}`
	var boxed types.AnyModel
	if err := json.Unmarshal([]byte(imageJSON), &boxed); err != nil {
		t.Fatal(err)
	}
	if boxed.Type != "image" || boxed.Image == nil {
		t.Fatalf("image union not decoded: %+v", boxed)
	}
	if len(boxed.Image.Output) != 1 || boxed.Image.Output[0] != types.ImagesModelOutputImage {
		t.Fatalf("image output lost: %+v", boxed.Image.Output)
	}
	if string(boxed.Raw()) != imageJSON {
		t.Fatalf("raw image object not preserved: %s", boxed.Raw())
	}

	classifierJSON := `{"type":"classifier","id":"cls","name":"Cls","api":"typesafe-system-one","provider":"p","baseUrl":"https://x","input":["text"],"cost":{},"contextWindow":4096}`
	var cls types.AnyModel
	if err := json.Unmarshal([]byte(classifierJSON), &cls); err != nil {
		t.Fatal(err)
	}
	if cls.Type != "classifier" || cls.Classifier == nil || cls.Classifier.ContextWindow != 4096 {
		t.Fatalf("classifier union not decoded: %+v", cls)
	}

	unknownJSON := `{"type":"future","id":"f","provider":"p"}`
	var unknown types.AnyModel
	if err := json.Unmarshal([]byte(unknownJSON), &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown.Type != "future" {
		t.Fatalf("unknown kind lost: %q", unknown.Type)
	}
	encoded, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != unknownJSON {
		t.Fatalf("unknown union not preserved: %s", encoded)
	}
}

// TestPiV1FoundationProviderHeaderMerge covers case-insensitive merging, null
// deletion, empty results and caller non-mutation.
func TestPiV1FoundationProviderHeaderMerge(t *testing.T) {
	first, second, last := "first", "second", "last"
	firstSource := types.ProviderHeaders{"X-Token": &first, "Keep": &first}
	secondSource := types.ProviderHeaders{"x-token": &second, "KEEP": nil}
	thirdSource := types.ProviderHeaders{"X-TOKEN": nil, "Another": &last}

	merged := utils.ProviderHeadersToRecord(firstSource, secondSource, thirdSource)
	if merged["Another"] != "last" {
		t.Fatalf("later spelling must win: %v", merged)
	}
	if _, ok := merged["X-TOKEN"]; ok {
		t.Fatalf("null override must delete the header: %v", merged)
	}
	if _, ok := merged["x-token"]; ok {
		t.Fatalf("null override must delete the lower-case spelling too: %v", merged)
	}
	if len(merged) != 1 {
		t.Fatalf("unexpected merged headers: %v", merged)
	}

	// Caller maps must not be mutated.
	if _, ok := firstSource["X-Token"]; !ok {
		t.Fatal("first source was mutated")
	}
	if _, ok := secondSource["x-token"]; !ok {
		t.Fatal("second source was mutated")
	}

	if got := utils.ProviderHeadersToRecord(types.ProviderHeaders{"x": nil}); got != nil {
		t.Fatalf("all-null suppression must return nil, got %v", got)
	}
	if got := utils.ProviderHeadersToRecord(); got != nil {
		t.Fatalf("no sources must return nil, got %v", got)
	}
	// A later non-null spelling replaces the earlier value and keeps its name.
	kept := utils.ProviderHeadersToRecord(types.ProviderHeaders{"A": &first}, types.ProviderHeaders{"a": &second})
	if kept["a"] != "second" {
		t.Fatalf("later non-null value must win: %v", kept)
	}
}

// TestPiV1FoundationRetryAndOverflow covers the new retry classification codes
// and the Z.AI CN overflow pattern.
func TestPiV1FoundationRetryAndOverflow(t *testing.T) {
	classify := func(message string) bool {
		assistant := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderOpenAI, "fixture", 1)
		assistant.StopReason = types.StopReasonError
		assistant.ErrorMessage = &message
		return utils.IsRetryableAssistantError(assistant)
	}
	for _, message := range []string{"subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable"} {
		if !classify(message) {
			t.Fatalf("%q must be retryable", message)
		}
	}
	for _, message := range []string{"subscription_sharing_usage_limit_exceeded", "billing quota exceeded"} {
		if classify(message) {
			t.Fatalf("%q must not be retryable", message)
		}
	}

	overflow := `{"code":"1261","message":"Prompt exceeds max length"}`
	assistant := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderId("zai-cn"), "fixture", 1)
	assistant.StopReason = types.StopReasonError
	assistant.ErrorMessage = &overflow
	if !utils.IsContextOverflow(assistant, nil) {
		t.Fatal("Z.AI CN overflow must be detected")
	}
}

// TestPiV1FoundationCatalogKinds checks the three V1 kinds, unknown lookups,
// immutable reads and same-id cross-kind isolation using the embedded release
// assets only (no network).
func TestPiV1FoundationCatalogKinds(t *testing.T) {
	for _, provider := range []string{"deepseek", "openai", "anthropic"} {
		seen := map[string]map[string]string{}
		for _, kind := range []string{"chat", "image", "classifier"} {
			records := catalog.V1Models(provider, kind)
			for _, raw := range records {
				var record struct {
					Id   string `json:"id"`
					Api  string `json:"api"`
					Type string `json:"type"`
				}
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatalf("%s/%s record is not JSON: %v", provider, kind, err)
				}
				if record.Id == "" || record.Api == "" {
					t.Fatalf("%s/%s record missing identity: %s", provider, kind, raw)
				}
				if record.Type != kind {
					t.Fatalf("%s/%s record has type %q: %s", provider, kind, record.Type, raw)
				}
				if seen[kind] == nil {
					seen[kind] = map[string]string{}
				}
				if previous, duplicate := seen[kind][record.Id]; duplicate && previous != record.Api {
					t.Fatalf("%s/%s duplicate id across APIs: %s", provider, kind, record.Id)
				}
				seen[kind][record.Id] = record.Api
			}
		}
		if len(seen["chat"]) == 0 {
			t.Fatalf("%s has no chat models", provider)
		}
	}

	if got := catalog.V1Models("does-not-exist", "chat"); len(got) != 0 {
		t.Fatalf("unknown provider must be empty, got %d", len(got))
	}
	if got := catalog.V1Models("deepseek", "unknown-kind"); len(got) != 0 {
		t.Fatalf("unknown kind must be empty, got %d", len(got))
	}

	// Defensive copies: mutating the returned slice must not corrupt the catalog.
	records := catalog.V1Models("deepseek", "chat")
	if len(records) == 0 {
		t.Fatal("deepseek chat catalog is empty")
	}
	original := string(records[0])
	records[0][0] = '!'
	again := catalog.V1Models("deepseek", "chat")
	if len(again) == 0 || string(again[0]) != original {
		t.Fatal("returned catalog record was not a defensive copy")
	}
	if !json.Valid(again[0]) {
		t.Fatal("catalog record is not valid JSON after caller mutation")
	}

	// Original JSON fields, including null thinking-level mappings, are retained.
	foundNullMapping := false
	for _, raw := range again {
		if strings.Contains(string(raw), `"thinkingLevelMap"`) && strings.Contains(string(raw), `null`) {
			foundNullMapping = true
			break
		}
	}
	if !foundNullMapping {
		t.Fatal("null thinking-level mappings were not preserved")
	}
}

// TestPiV1FoundationManifest checks the pinned manifest is a defensive copy with
// its provenance intact.
func TestPiV1FoundationManifest(t *testing.T) {
	manifest := catalog.V1Manifest()
	if len(manifest) == 0 {
		t.Fatal("V1 manifest is empty")
	}
	if !json.Valid(manifest) {
		t.Fatal("V1 manifest is not valid JSON")
	}
	var decoded map[string]any
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["schemaVersion"]; !ok {
		t.Fatal("manifest is missing schemaVersion")
	}
	if _, ok := decoded["structureHash"]; !ok {
		t.Fatal("manifest is missing structureHash")
	}
	if _, ok := decoded["files"]; !ok {
		t.Fatal("manifest is missing per-provider digests")
	}

	manifest[0] = '!'
	if !json.Valid(catalog.V1Manifest()) {
		t.Fatal("manifest was not a defensive copy")
	}
}
