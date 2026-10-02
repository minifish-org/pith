// This file holds the offline candidate self-tests for the Pi 1.0 mixed model
// runtime and the native classifier protocols. Every test runs against an
// httptest server or the in-memory runtime and never touches the network.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/auth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
)

func classifierAPIKey() *string {
	key := "fake"
	return &key
}

func classifierModel(baseURL string) types.ClassifierModel {
	return types.ClassifierModel{
		Type:          types.ModelTypeClassifier,
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.ClassifierApiTypesafeSystemOne,
		Provider:      types.ProviderId("typesafe"),
		BaseUrl:       baseURL,
		Input:         []types.ModelInputModality{types.ModelInputText},
		Cost:          types.ModelCost{ModelCostRates: types.ModelCostRates{Input: 1, Output: 2}},
		ContextWindow: 64000,
	}
}

func boolAndChoiceContext() types.ClassifierContext {
	return types.ClassifierContext{
		State: types.JsonObject{"task": "example"},
		Questions: map[string]types.ClassifierQuestion{
			"yes": {
				Type:         "bool",
				Instructions: "valid?",
				Criteria:     json.RawMessage(`{"true":"yes","false":"no"}`),
			},
			"category": {
				Type:         "choice",
				Instructions: "category?",
				Criteria:     json.RawMessage(`{"a":"first","b":"second"}`),
			},
		},
	}
}

// TestPiV1SystemOneWire covers the TypeSafe wire mapping: public bool becomes
// `noul`, the request envelope carries the model id and the response answers are
// normalized back to the public union with priced usage.
func TestPiV1SystemOneWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q; want /v1/systemone", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fake" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		questions, _ := body["questions"].(map[string]any)
		yes, _ := questions["yes"].(map[string]any)
		if body["model"] != "fixture" || yes["type"] != "noul" {
			t.Errorf("wire mapping wrong: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"yes":{"type":"noul","noul":0.75},"category":{"type":"choice","choice":"a","probabilities":{"a":0.8,"b":0.2},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()

	model := classifierModel(server.URL + "/v1")
	request := boolAndChoiceContext()
	result := api.TypesafeSystemOneClassify(context.Background(), &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: classifierAPIKey()}})

	if result.StopReason != types.ClassifierStopReasonStop {
		t.Fatalf("stop reason = %q (%v)", result.StopReason, result.ErrorMessage)
	}
	if result.Answers["yes"].Type != "bool" || result.Answers["yes"].Probability == nil || *result.Answers["yes"].Probability != 0.75 {
		t.Fatalf("bool answer = %+v", result.Answers["yes"])
	}
	if result.Answers["category"].Choice == nil || *result.Answers["category"].Choice != "a" {
		t.Fatalf("choice answer = %+v", result.Answers["category"])
	}
	if result.Usage == nil || result.Usage.Input != 10 || result.Usage.Output != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if diff := result.Usage.Cost.Total - (10*1+2*2)/1000000.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v", result.Usage.Cost.Total)
	}
}

// TestPiV1SystemOneMissingAnswerFailsWholeResult verifies a partial response is
// rejected instead of returning fabricated answers, while keeping the billed
// usage.
func TestPiV1SystemOneMissingAnswerFailsWholeResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"yes":{"type":"noul","noul":0.75}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()

	model := classifierModel(server.URL + "/v1")
	request := boolAndChoiceContext()
	result := api.TypesafeSystemOneClassify(context.Background(), &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: classifierAPIKey()}})

	if result.StopReason != types.ClassifierStopReasonError {
		t.Fatalf("stop reason = %q; want error", result.StopReason)
	}
	if len(result.Answers) != 0 {
		t.Fatalf("answers = %+v; want none", result.Answers)
	}
	if result.Usage == nil || result.Usage.Input != 10 {
		t.Fatalf("billed usage lost: %+v", result.Usage)
	}
}

// TestPiV1SystemOneUnsupportedApiAndMissingKey covers the two pre-request
// failures: a mismatched api and a missing API key never reach the network.
func TestPiV1SystemOneUnsupportedApiAndMissingKey(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	model := classifierModel(server.URL + "/v1")
	model.Api = types.ClassifierApiCloudflareWorkersAISystemOne
	request := boolAndChoiceContext()
	result := api.TypesafeSystemOneClassify(context.Background(), &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: classifierAPIKey()}})
	if result.StopReason != types.ClassifierStopReasonError || !strings.Contains(errorText(result), "Unsupported classifier API") {
		t.Fatalf("unsupported api result = %+v", result)
	}

	model = classifierModel(server.URL + "/v1")
	result = api.TypesafeSystemOneClassify(context.Background(), &model, &request, nil)
	if result.StopReason != types.ClassifierStopReasonError || !strings.Contains(errorText(result), "No API key") {
		t.Fatalf("missing key result = %+v", result)
	}
	if requests != 0 {
		t.Fatalf("requests = %d; want 0", requests)
	}
}

// TestPiV1CloudflareEnvelope covers the Workers AI REST envelope unwrapping and
// the `success: false` error path.
func TestPiV1CloudflareEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/run") {
			t.Errorf("path = %q; want .../run", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		input, _ := body["input"].(map[string]any)
		if input == nil {
			t.Errorf("missing input envelope: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"state":"Completed","result":{"answers":{"yes":{"type":"noul","noul":0.25}}}}}`))
	}))
	defer server.Close()

	model := classifierModel(server.URL)
	model.Api = types.ClassifierApiCloudflareWorkersAISystemOne
	model.Provider = types.ProviderCloudflareWorkersAI
	request := types.ClassifierContext{
		State: types.JsonObject{},
		Questions: map[string]types.ClassifierQuestion{
			"yes": {Type: "bool", Instructions: "ok?", Criteria: json.RawMessage(`{"true":"y","false":"n"}`)},
		},
	}
	result := api.CloudflareWorkersAISystemOneClassify(context.Background(), &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: classifierAPIKey()}})
	if result.StopReason != types.ClassifierStopReasonStop {
		t.Fatalf("stop reason = %q (%v)", result.StopReason, result.ErrorMessage)
	}
	if result.Answers["yes"].Probability == nil || *result.Answers["yes"].Probability != 0.25 {
		t.Fatalf("answer = %+v", result.Answers["yes"])
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"boom"}]}`))
	}))
	defer failing.Close()
	model = classifierModel(failing.URL)
	model.Api = types.ClassifierApiCloudflareWorkersAISystemOne
	result = api.CloudflareWorkersAISystemOneClassify(context.Background(), &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: classifierAPIKey()}})
	if result.StopReason != types.ClassifierStopReasonError || !strings.Contains(errorText(result), "boom") {
		t.Fatalf("failure result = %+v", result)
	}
}

// LLamaFakeServer is a minimal llama-server stand-in: tokenization by rune code
// point, a pass-through template and configurable next-token log-probabilities.
func newLlamaServer(t *testing.T, logprobs map[string]float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tokenize"):
			content, _ := body["content"].(string)
			tokens := make([]map[string]any, 0, len([]rune(content)))
			for _, char := range content {
				tokens = append(tokens, map[string]any{"id": int(char), "token": string(char)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tokens})
		case strings.HasSuffix(r.URL.Path, "/apply-template"):
			_ = json.NewEncoder(w).Encode(map[string]any{"prompt": "<|assistant|>\n"})
		case strings.HasSuffix(r.URL.Path, "/completion"):
			top := make([]map[string]any, 0, len(logprobs))
			for token, logprob := range logprobs {
				top = append(top, map[string]any{"id": int([]rune(token)[0]), "token": token, "logprob": logprob})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content":                  "A",
				"completion_probabilities": []map[string]any{{"id": 65, "token": "A", "top_logprobs": top}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestPiV1LlamaClassify reads the softmax over next-token log-probabilities
// instead of a free-text completion.
func TestPiV1LlamaClassify(t *testing.T) {
	server := newLlamaServer(t, map[string]float64{"A": -0.1, "B": -2.5})
	defer server.Close()

	model := classifierModel(server.URL + "/v1")
	model.Api = types.ClassifierApiLlamaCppClassify
	model.Provider = types.ProviderId("llama.cpp")
	model.Id = "qwen"
	request := types.ClassifierContext{
		State: types.JsonObject{"message": "hi"},
		Questions: map[string]types.ClassifierQuestion{
			"pick": {Type: "choice", Instructions: "Pick one", Criteria: json.RawMessage(`{"a":"first","b":"second"}`)},
		},
	}
	result := api.LlamaCppClassify(context.Background(), &model, &request, &types.ClassifierOptions{})
	if result.StopReason != types.ClassifierStopReasonStop {
		t.Fatalf("stop reason = %q (%v)", result.StopReason, result.ErrorMessage)
	}
	answer := result.Answers["pick"]
	if answer.Choice == nil || *answer.Choice != "a" || answer.Confidence == nil {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.Probabilities == nil || answer.Probabilities["a"] <= answer.Probabilities["b"] {
		t.Fatalf("probabilities = %+v", answer.Probabilities)
	}
}

// TestPiV1LlamaRejectsNonPositiveTemperature verifies validation happens before
// any request.
func TestPiV1LlamaRejectsNonPositiveTemperature(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
	}))
	defer server.Close()

	model := classifierModel(server.URL + "/v1")
	model.Api = types.ClassifierApiLlamaCppClassify
	temperature := 0.0
	request := types.ClassifierContext{State: types.JsonObject{}, Questions: map[string]types.ClassifierQuestion{
		"pick": {Type: "choice", Instructions: "Pick", Criteria: json.RawMessage(`{"a":"","b":""}`)},
	}}
	result := api.LlamaCppClassify(context.Background(), &model, &request, &types.ClassifierOptions{Temperature: &temperature})
	if result.StopReason != types.ClassifierStopReasonError || !strings.Contains(errorText(result), "Temperature must be a positive number") {
		t.Fatalf("result = %+v", result)
	}
	if requests != 0 {
		t.Fatalf("requests = %d; want 0", requests)
	}
}

func errorText(result types.ClassifierResult) string {
	if result.ErrorMessage == nil {
		return ""
	}
	return *result.ErrorMessage
}

// TestPiV1ModelIdentity covers the any-based runtime narrowing helpers,
// including cross-kind id collisions and raw JSON models.
func TestPiV1ModelIdentity(t *testing.T) {
	var a, b types.Model
	_ = json.Unmarshal([]byte(`{"id":"same","provider":"p","api":"a"}`), &a)
	_ = json.Unmarshal([]byte(`{"id":"same","provider":"p","api":"a","type":"chat"}`), &b)
	if !ModelsAreEqual(&a, &b) {
		t.Fatal("legacy and explicit chat identities differ")
	}
	image := json.RawMessage(`{"id":"same","provider":"p","api":"a","type":"image"}`)
	classifier := json.RawMessage(`{"id":"same","provider":"p","api":"a","type":"classifier"}`)
	if ModelsAreEqual(&a, image) || ModelsAreEqual(image, classifier) {
		t.Fatal("cross-kind ids incorrectly collide")
	}
	if HasApi(image, types.Api("a")) {
		t.Fatal("image must not satisfy chat api predicate")
	}
	if !HasApi(a, types.Api("a")) {
		t.Fatal("chat model must satisfy chat api predicate")
	}
}

// TestPiV1MixedProviderOverlay covers the mixed catalog: chat and classifier
// models that share an id, typed lookups and the type-aware dynamic overlay.
func TestPiV1MixedProviderOverlay(t *testing.T) {
	var classifier types.AnyModel
	_ = json.Unmarshal([]byte(`{"type":"classifier","id":"shared","provider":"p","api":"typesafe-system-one"}`), &classifier)
	chat := types.NewAnyChatModel(types.Model{Id: "shared", Provider: "p", Api: types.ApiOpenAICompletions})
	provider := CreateProvider(CreateProviderOptions{
		ID:        "p",
		Auth:      testAPIKeyAuth(),
		AllModels: []types.AnyModel{chat, classifier},
	})
	models := CreateModels(nil)
	models.SetProvider(provider)

	if got := models.GetModel("p", "shared"); got == nil || got.Api != types.ApiOpenAICompletions {
		t.Fatalf("chat lookup = %+v", got)
	}
	all := models.GetAllModels("p")
	if len(all) != 2 {
		t.Fatalf("all models = %d; want 2", len(all))
	}
	if chatModels := models.GetModelsOfType(types.ModelTypeChat, "p"); len(chatModels) != 1 {
		t.Fatalf("chat of type = %d", len(chatModels))
	}
	if classifiers := models.GetModelsOfType(types.ModelTypeClassifier, "p"); len(classifiers) != 1 {
		t.Fatalf("classifiers = %d", len(classifiers))
	}
	if got := models.GetModelOfType(types.ModelTypeClassifier, "p", "shared"); got == nil || types.GetModelType(*got) != types.ModelTypeClassifier {
		t.Fatalf("classifier lookup = %+v", got)
	}
	// A chat overlay must not displace the classifier with the same id.
	provider.(*providerImpl).setDynamic([]types.Model{{Id: "shared", Provider: "p", Api: types.ApiOpenAICompletions, Name: "overlaid"}})
	if got := models.GetModel("p", "shared"); got == nil || got.Name != "overlaid" {
		t.Fatalf("overlay not applied: %+v", got)
	}
	if classifiers := models.GetModelsOfType(types.ModelTypeClassifier, "p"); len(classifiers) != 1 {
		t.Fatalf("classifier removed by chat overlay: %d", len(classifiers))
	}
}

// TestPiV1ClassifierDispatchThroughModels covers the runtime classify dispatch
// and the unsupported-provider / unknown-provider error results.
func TestPiV1ClassifierDispatchThroughModels(t *testing.T) {
	key := "key"
	implementation := func(_ context.Context, model types.ClassifierModel, _ types.ClassifierContext, _ *types.ClassifierOptions) types.ClassifierResult {
		answer := 1.0
		return types.ClassifierResult{Api: model.Api, Provider: model.Provider, Model: model.Id, Answers: map[string]types.ClassifierAnswer{
			"yes": {Type: "bool", Probability: &answer},
		}, StopReason: types.ClassifierStopReasonStop, Timestamp: 1}
	}
	provider := CreateProvider(CreateProviderOptions{
		ID:   "p",
		Auth: testAPIKeyAuth(),
		AllModels: []types.AnyModel{
			types.NewAnyClassifierModel(types.ClassifierModel{Id: "c", Provider: "p", Api: types.ClassifierApiTypesafeSystemOne}),
		},
		Classifiers: map[types.ClassifierApi]ClassifierImplementation{
			types.ClassifierApiTypesafeSystemOne: implementation,
		},
	})
	models := CreateModels(&CreateModelsOptions{Credentials: keyCredentials(t, "p", key)})
	models.SetProvider(provider)

	result := models.Classify(context.Background(), types.ClassifierModel{Id: "c", Provider: "p", Api: types.ClassifierApiTypesafeSystemOne}, types.ClassifierContext{}, nil)
	if result.StopReason != types.ClassifierStopReasonStop || result.Answers["yes"].Probability == nil {
		t.Fatalf("classify result = %+v", result)
	}

	unsupported := models.Classify(context.Background(), types.ClassifierModel{Id: "c", Provider: "p", Api: types.ClassifierApi("other")}, types.ClassifierContext{}, nil)
	if unsupported.StopReason != types.ClassifierStopReasonError {
		t.Fatalf("unsupported result = %+v", unsupported)
	}

	unknown := models.Classify(context.Background(), types.ClassifierModel{Id: "c", Provider: "unknown", Api: types.ClassifierApiTypesafeSystemOne}, types.ClassifierContext{}, nil)
	if unknown.StopReason != types.ClassifierStopReasonError || !strings.Contains(errorText(unknown), "Unknown provider") {
		t.Fatalf("unknown provider result = %+v", unknown)
	}
}

// TestPiV1StorePreservesMixedMetadataAndEtags covers the persisted catalog:
// classifier elements survive a store restart verbatim and the ETag/checkedAt
// metadata round-trips.
func TestPiV1StorePreservesMixedMetadataAndEtags(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryModelsStore()
	var entry ModelsStoreEntry
	raw := `{"models":[{"type":"classifier","id":"c","provider":"p","api":"typesafe-system-one"},{"id":"chat","provider":"p","api":"openai-completions"}],"etag":"\"v1\"","checkedAt":5}`
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if err := store.Write(ctx, "p", &entry, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	stored, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored.Etag == nil || *stored.Etag != "\"v1\"" || stored.CheckedAt == nil || *stored.CheckedAt != 5 {
		t.Fatalf("metadata lost: %+v", stored)
	}
	decoded := storedAnyModels(stored)
	if len(decoded) != 2 {
		t.Fatalf("decoded models = %d; want 2", len(decoded))
	}
	var kinds []string
	for _, model := range decoded {
		kinds = append(kinds, types.GetModelType(model))
	}
	if kinds[0] != types.ModelTypeClassifier || kinds[1] != types.ModelTypeChat {
		t.Fatalf("kinds = %v", kinds)
	}
}

// TestPiV1AllAvailableOfType covers the credential-specific availability across
// model kinds.
func TestPiV1AllAvailableOfType(t *testing.T) {
	provider := CreateProvider(CreateProviderOptions{
		ID:   "p",
		Auth: testAPIKeyAuth(),
		AllModels: []types.AnyModel{
			types.NewAnyChatModel(types.Model{Id: "chat", Provider: "p"}),
			types.NewAnyClassifierModel(types.ClassifierModel{Id: "classifier", Provider: "p", Api: types.ClassifierApiTypesafeSystemOne}),
		},
	})
	models := CreateModels(&CreateModelsOptions{Credentials: keyCredentials(t, "p", "key")})
	models.SetProvider(provider)

	all, err := models.GetAllAvailable(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("getAllAvailable: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all available = %d; want 2", len(all))
	}
	classifiers, err := models.GetAvailableOfType(context.Background(), types.ModelTypeClassifier, "p", nil)
	if err != nil {
		t.Fatalf("getAvailableOfType: %v", err)
	}
	if len(classifiers) != 1 || types.GetModelType(classifiers[0]) != types.ModelTypeClassifier {
		t.Fatalf("available classifiers = %+v", classifiers)
	}
}

// TestPiV1ClassifierCancellation covers abort handling: an already-cancelled
// context aborts before any request is issued.
func TestPiV1ClassifierCancellation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
	}))
	defer server.Close()

	model := classifierModel(server.URL + "/v1")
	request := boolAndChoiceContext()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	cancel()
	result := api.TypesafeSystemOneClassify(ctx, &model, &request, &types.ClassifierOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: classifierAPIKey()}})
	if result.StopReason != types.ClassifierStopReasonAborted {
		t.Fatalf("stop reason = %q; want aborted", result.StopReason)
	}
	if requests != 0 {
		t.Fatalf("requests = %d; want 0", requests)
	}
}

func keyCredentials(t *testing.T, providerID string, key string) authtypes.CredentialStore {
	t.Helper()
	store := auth.NewInMemoryCredentialStore()
	if _, err := store.Modify(context.Background(), providerID, func(authtypes.Credential) (authtypes.Credential, error) {
		return authtypes.NewApiKeyCredential(key), nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	return store
}
