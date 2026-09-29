package ai

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/auth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

func strPtr(value string) *string { return &value }

func floatPtr(value float64) *float64 { return &value }

func boolPtr(value bool) *bool { return &value }

// testAPIKeyAuth returns an api-key auth whose Resolve always succeeds, so the
// refresh path can resolve a credential without any environment or network.
func testAPIKeyAuth() authtypes.ProviderAuth {
	return authtypes.ProviderAuth{APIKey: &authtypes.ApiKeyAuth{
		Name: "Test key",
		Resolve: func(_ context.Context, _ authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
			key := "test-key"
			source := "test"
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{APIKey: &key}, Source: &source}, nil
		},
	}}
}

// TestInMemoryModelsStoreIsolation mirrors the model-store conformance scenario:
// writes and reads are structural clones, so mutating the caller's object or a
// read result never changes the stored catalog, and delete makes reads return
// nil.
func TestInMemoryModelsStoreIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryModelsStore()

	var entry ModelsStoreEntry
	if err := json.Unmarshal([]byte(`{"models":[{"id":"one"}],"etag":"\"v1\"","checkedAt":0}`), &entry); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if err := store.Write(ctx, "p", &entry, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	entry.Models[0].Id = "wrong"

	first, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if first.Models[0].Id != "one" {
		t.Fatalf("read after caller mutation = %q; want one", first.Models[0].Id)
	}
	first.Models[0].Id = "wrong2"

	second, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if second.Models[0].Id != "one" {
		t.Fatalf("reread after read mutation = %q; want one", second.Models[0].Id)
	}

	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"models":[{"id":"one"}],"checkedAt":0,"etag":"\"v1\""}`
	var gotValue, wantValue any
	if err := json.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if got, wantJSON := mustJSON(t, gotValue), mustJSON(t, wantValue); got != wantJSON {
		t.Fatalf("stored entry = %s; want %s", got, wantJSON)
	}

	if err := store.Delete(ctx, "p", nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleted, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatalf("read after delete: %v", err)
	}
	if deleted != nil {
		t.Fatalf("read after delete = %+v; want nil", deleted)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestInMemoryModelsStoreAbort verifies an already-cancelled context stops a
// store operation.
func TestInMemoryModelsStoreAbort(t *testing.T) {
	store := NewInMemoryModelsStore()
	aborted, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Read(aborted, "p", &ModelsStoreOperationOptions{Signal: aborted}); err == nil {
		t.Fatal("read with cancelled context succeeded; want error")
	}
	if err := store.Write(aborted, "p", &ModelsStoreEntry{}, &ModelsStoreOperationOptions{Signal: aborted}); err == nil {
		t.Fatal("write with cancelled context succeeded; want error")
	}
	if err := store.Delete(aborted, "p", &ModelsStoreOperationOptions{Signal: aborted}); err == nil {
		t.Fatal("delete with cancelled context succeeded; want error")
	}
}

// TestSessionResourceCleanup ports the session-resources contract: cleanups run
// once in registration order, an unsubscribe removes a cleanup, and failures
// are aggregated.
func TestSessionResourceCleanup(t *testing.T) {
	var order []string
	removeSkipped := RegisterSessionResourceCleanup(func(*string) error {
		order = append(order, "skipped")
		return nil
	})
	RegisterSessionResourceCleanup(func(*string) error {
		order = append(order, "first")
		return errors.New("boom")
	})
	removeSecond := RegisterSessionResourceCleanup(func(sessionID *string) error {
		if sessionID == nil || *sessionID != "session-1" {
			t.Fatalf("cleanup session id = %v", sessionID)
		}
		order = append(order, "second")
		return nil
	})
	removeSkipped()

	sessionID := "session-1"
	err := CleanupSessionResources(&sessionID)
	var aggregate *AggregateCleanupError
	if !errors.As(err, &aggregate) {
		t.Fatalf("cleanup error = %v; want *AggregateCleanupError", err)
	}
	if len(aggregate.Errors) != 1 {
		t.Fatalf("aggregate errors = %d; want 1", len(aggregate.Errors))
	}
	if want := []string{"first", "second"}; !equalStrings(order, want) {
		t.Fatalf("cleanup order = %v; want %v", order, want)
	}
	removeSecond()

	// Unsubscribing is idempotent. "first" is still registered, so a later pass
	// runs it again; "skipped" never runs.
	removeSkipped()
	err = CleanupSessionResources(nil)
	if !errors.As(err, &aggregate) {
		t.Fatalf("second cleanup error = %v; want *AggregateCleanupError", err)
	}
	if containsString(order, "skipped") {
		t.Fatalf("unsubscribed cleanup ran: %v", order)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func closeEnough(got, want float64) bool {
	difference := got - want
	if difference < 0 {
		difference = -difference
	}
	return difference < 1e-9
}

// TestCalculateCost covers the flat rates, the request-wide tier selection and
// the 2x base input for 1h cache writes.
func TestCalculateCost(t *testing.T) {
	model := types.Model{Cost: types.ModelCost{
		ModelCostRates: types.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
		Tiers: []types.ModelCostTier{
			{ModelCostRates: types.ModelCostRates{Input: 6, Output: 30, CacheRead: 0.6, CacheWrite: 7.5}, InputTokensAbove: 200000},
		},
	}}

	usage := types.Usage{Input: 100000, Output: 1000, CacheRead: 0, CacheWrite: 0}
	cost := CalculateCost(model, &usage)
	if !closeEnough(cost.Input, 0.3) || !closeEnough(cost.Output, 0.015) || !closeEnough(cost.Total, 0.315) {
		t.Fatalf("flat cost = %+v", cost)
	}

	// Above the tier threshold the whole request uses the higher rates.
	tierUsage := types.Usage{Input: 300000, Output: 1000}
	tierCost := CalculateCost(model, &tierUsage)
	if !closeEnough(tierCost.Input, 1.8) || !closeEnough(tierCost.Output, 0.03) || !closeEnough(tierCost.Total, 1.83) {
		t.Fatalf("tier cost = %+v", tierCost)
	}

	// 1h cache writes double base input.
	longWrite := 1000.0
	cacheUsage := types.Usage{Input: 0, Output: 0, CacheWrite: 2000, CacheWrite1h: &longWrite}
	cacheCost := CalculateCost(model, &cacheUsage)
	// short write 1000 * 3.75/1e6 + long write 1000 * (3*2)/1e6
	if !closeEnough(cacheCost.CacheWrite, (1000*3.75+1000*6)/1000000) {
		t.Fatalf("cache write cost = %v", cacheCost.CacheWrite)
	}
}

// TestThinkingLevelsFromCatalog ports the github-copilot-anthropic reference
// scenarios: xhigh/max require an explicit mapping, and a null mapping marks a
// level unsupported.
func TestThinkingLevelsFromCatalog(t *testing.T) {
	reasoning := func(levelMap types.ThinkingLevelMap) types.Model {
		return types.Model{Reasoning: true, ThinkingLevelMap: levelMap}
	}

	opus47 := reasoning(types.ThinkingLevelMap{
		types.ThinkingXHigh:   strPtr("xhigh"),
		types.ThinkingMax:     strPtr("max"),
		types.ThinkingMinimal: strPtr("low"),
	})
	levels47 := GetSupportedThinkingLevels(opus47)
	if !containsThinkingLevel(levels47, types.ThinkingXHigh) || !containsThinkingLevel(levels47, types.ThinkingMax) {
		t.Fatalf("opus-4.7 levels = %v; want xhigh and max", levels47)
	}

	sonnet46 := reasoning(types.ThinkingLevelMap{
		types.ThinkingMax:     strPtr("max"),
		types.ThinkingMinimal: strPtr("low"),
	})
	levels46 := GetSupportedThinkingLevels(sonnet46)
	if !containsThinkingLevel(levels46, types.ThinkingMax) {
		t.Fatalf("sonnet-4.6 levels = %v; want max", levels46)
	}
	if containsThinkingLevel(levels46, types.ThinkingXHigh) {
		t.Fatalf("sonnet-4.6 levels = %v; want no xhigh", levels46)
	}

	// opus-5.5 explicitly marks off and minimal unsupported.
	opus55 := reasoning(types.ThinkingLevelMap{
		types.ThinkingOff:     nil,
		types.ThinkingMinimal: nil,
		types.ThinkingLow:     strPtr("low"),
		types.ThinkingMedium:  strPtr("medium"),
		types.ThinkingHigh:    strPtr("high"),
		types.ThinkingXHigh:   strPtr("xhigh"),
		types.ThinkingMax:     strPtr("max"),
	})
	levels55 := GetSupportedThinkingLevels(opus55)
	want55 := []types.ModelThinkingLevel{types.ThinkingLow, types.ThinkingMedium, types.ThinkingHigh, types.ThinkingXHigh, types.ThinkingMax}
	if len(levels55) != len(want55) {
		t.Fatalf("opus-5.5 levels = %v; want %v", levels55, want55)
	}
	for i := range want55 {
		if levels55[i] != want55[i] {
			t.Fatalf("opus-5.5 levels = %v; want %v", levels55, want55)
		}
	}

	if levels := GetSupportedThinkingLevels(types.Model{Reasoning: false}); len(levels) != 1 || levels[0] != types.ThinkingOff {
		t.Fatalf("non-reasoning levels = %v; want [off]", levels)
	}

	if got := ClampThinkingLevel(opus55, types.ThinkingMax); got != types.ThinkingMax {
		t.Fatalf("clamp max = %q", got)
	}
	if got := ClampThinkingLevel(sonnet46, types.ThinkingXHigh); got != types.ThinkingMax {
		t.Fatalf("clamp xhigh = %q; want max", got)
	}
	if got := ClampThinkingLevel(opus55, "bogus"); got != types.ThinkingLow {
		t.Fatalf("clamp unknown = %q; want low", got)
	}
}

// TestModelsAreEqualAndHasApi covers the two runtime narrowing helpers.
func TestModelsAreEqualAndHasApi(t *testing.T) {
	first := types.Model{Id: "m", Provider: "p", Api: types.ApiAnthropicMessages}
	same := types.Model{Id: "m", Provider: "p", Api: types.ApiOpenAICompletions}
	other := types.Model{Id: "m", Provider: "q"}

	if !ModelsAreEqual(&first, &same) {
		t.Fatal("modelsAreEqual ignored api difference; want equal")
	}
	if ModelsAreEqual(&first, &other) {
		t.Fatal("modelsAreEqual with different provider; want false")
	}
	if ModelsAreEqual(&first, nil) {
		t.Fatal("modelsAreEqual with nil; want false")
	}
	if !HasApi(first, types.ApiAnthropicMessages) {
		t.Fatal("hasApi anthropic-messages; want true")
	}
	if HasApi(first, types.ApiOpenAICompletions) {
		t.Fatal("hasApi openai-completions; want false")
	}
}

// TestProviderDynamicOverlayAndRefresh ports the radius-provider reference
// scenarios without network: a static catalog, a refreshed overlay keyed by id,
// and a cached catalog restored with network disabled.
func TestProviderDynamicOverlayAndRefresh(t *testing.T) {
	ctx := context.Background()
	baseline := []types.Model{
		{Id: "balanced", Name: "Static Balanced", Provider: "radius", Api: types.ApiPiMessages, Cost: types.ModelCost{ModelCostRates: types.ModelCostRates{Input: 1, Output: 2}}},
		{Id: "legacy", Name: "Legacy", Provider: "radius", Api: types.ApiPiMessages},
	}
	refreshed := []types.Model{
		{Id: "balanced", Name: "Fresh Balanced", Provider: "radius", Api: types.ApiPiMessages, ContextWindow: 424242},
		{Id: "organization-only", Name: "Organization Only", Provider: "radius", Api: types.ApiPiMessages},
	}

	fetchCalls := 0
	provider := CreateProvider(CreateProviderOptions{
		ID:      "radius",
		Auth:    testAPIKeyAuth(),
		Models:  baseline,
		BaseURL: "https://radius.example/v1",
		FetchModels: func(_ context.Context, _ *RefreshModelsContext) ([]types.Model, error) {
			fetchCalls++
			return refreshed, nil
		},
	})

	if !provider.SupportsRefreshModels() {
		t.Fatal("provider with FetchModels not refreshable")
	}
	store := NewInMemoryModelsStore()
	models := CreateModels(&CreateModelsOptions{ModelsStore: store})
	models.SetProvider(provider)

	result, err := models.Refresh(ctx, &ModelsRefreshOptions{Providers: []string{"radius"}})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Aborted || len(result.Errors) != 0 {
		t.Fatalf("refresh result = %+v", result)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d; want 1", fetchCalls)
	}
	balanced := models.GetModel("radius", "balanced")
	if balanced == nil || balanced.Name != "Fresh Balanced" || balanced.ContextWindow != 424242 {
		t.Fatalf("overlaid balanced = %+v", balanced)
	}
	if models.GetModel("radius", "organization-only") == nil {
		t.Fatal("overlaid model missing")
	}
	if models.GetModel("radius", "legacy") == nil {
		t.Fatal("static baseline model missing after overlay")
	}

	// A second refresh with network disabled restores the cached overlay into a
	// fresh collection and never calls fetch.
	cachedStore := NewInMemoryModelsStore()
	checkedAt := float64(time.Now().UnixMilli())
	if err := cachedStore.Write(ctx, "radius", &ModelsStoreEntry{Models: refreshed, CheckedAt: &checkedAt}, nil); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	cachedFetchCalls := 0
	cachedProvider := CreateProvider(CreateProviderOptions{
		ID:     "radius",
		Auth:   testAPIKeyAuth(),
		Models: baseline,
		FetchModels: func(_ context.Context, _ *RefreshModelsContext) ([]types.Model, error) {
			cachedFetchCalls++
			return refreshed, nil
		},
	})
	cachedModels := CreateModels(&CreateModelsOptions{ModelsStore: cachedStore})
	cachedModels.SetProvider(cachedProvider)
	if _, err := cachedModels.Refresh(ctx, &ModelsRefreshOptions{Providers: []string{"radius"}, AllowNetwork: boolPtr(false)}); err != nil {
		t.Fatalf("cached refresh: %v", err)
	}
	if cachedFetchCalls != 0 {
		t.Fatalf("cached refresh fetch calls = %d; want 0", cachedFetchCalls)
	}
	if got := cachedModels.GetModel("radius", "balanced"); got == nil || got.Name != "Fresh Balanced" {
		t.Fatalf("cached balanced = %+v", got)
	}
	if got := cachedModels.GetModel("radius", "organization-only"); got == nil {
		t.Fatal("cached organization-only missing")
	}
}

// TestProviderRefreshErrorIsRecorded verifies provider failures are reported in
// the result instead of rejecting it.
func TestProviderRefreshErrorIsRecorded(t *testing.T) {
	provider := CreateProvider(CreateProviderOptions{
		ID:   "p",
		Auth: testAPIKeyAuth(),
		FetchModels: func(_ context.Context, _ *RefreshModelsContext) ([]types.Model, error) {
			return nil, errors.New("network down")
		},
	})
	models := CreateModels(nil)
	models.SetProvider(provider)
	result, err := models.Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("refresh returned error: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("refresh errors = %v; want 1", result.Errors)
	}
	if _, ok := result.Errors["p"]; !ok {
		t.Fatalf("refresh errors missing provider p: %v", result.Errors)
	}
}

// TestRefreshGenerationsSupersede verifies a superseded refresh cannot publish
// stale state: setting a new provider aborts the old refresh before it writes.
func TestRefreshGenerationsSupersede(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryModelsStore()

	block := make(chan struct{})
	started := make(chan struct{})
	provider := CreateProvider(CreateProviderOptions{
		ID:   "p",
		Auth: testAPIKeyAuth(),
		FetchModels: func(fetchCtx context.Context, _ *RefreshModelsContext) ([]types.Model, error) {
			close(started)
			select {
			case <-block:
			case <-fetchCtx.Done():
				return nil, fetchCtx.Err()
			}
			return []types.Model{{Id: "stale", Provider: "p"}}, nil
		},
	})
	models := CreateModels(&CreateModelsOptions{ModelsStore: store})
	models.SetProvider(provider)

	done := make(chan *ModelsRefreshResult, 1)
	go func() {
		result, _ := models.Refresh(ctx, nil)
		done <- result
	}()
	<-started
	// Replacing the provider supersedes the in-flight refresh.
	models.SetProvider(CreateProvider(CreateProviderOptions{ID: "p", Auth: testAPIKeyAuth()}))
	close(block)
	<-done

	stored, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if stored != nil {
		t.Fatalf("superseded refresh published %+v; want nothing", stored)
	}
}

// TestProviderFilterModels verifies the optional credential filter is only
// applied when present and is surfaced through getAvailable.
func TestProviderFilterModels(t *testing.T) {
	provider := CreateProvider(CreateProviderOptions{
		ID:     "p",
		Auth:   testAPIKeyAuth(),
		Models: []types.Model{{Id: "a", Provider: "p"}, {Id: "b", Provider: "p"}},
		FilterModels: func(models []types.Model, _ authtypes.Credential) []types.Model {
			return []types.Model{models[0]}
		},
	})
	credentials := auth.NewInMemoryCredentialStore()
	models := CreateModels(&CreateModelsOptions{Credentials: credentials})
	models.SetProvider(provider)

	available, err := models.GetAvailable(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("getAvailable: %v", err)
	}
	if len(available) != 1 || available[0].Id != "a" {
		t.Fatalf("available = %+v; want [a]", available)
	}
}

// TestModelsAuthApplication verifies stream dispatch resolves auth, applies the
// model's static headers and lets explicit options win.
func TestModelsAuthApplication(t *testing.T) {
	var captured authCapture
	credentials := auth.NewInMemoryCredentialStore()
	if _, err := credentials.Modify(context.Background(), "p", func(authtypes.Credential) (authtypes.Credential, error) {
		return authtypes.NewApiKeyCredential("stored-key"), nil
	}, nil); err != nil {
		t.Fatal(err)
	}

	streams := &capturingStreams{capture: &captured}
	provider := CreateProvider(CreateProviderOptions{
		ID:     "p",
		Auth:   testAPIKeyAuth(),
		Models: []types.Model{{Id: "m", Provider: "p", Api: types.ApiAnthropicMessages}},
		API:    streams,
	})
	models := CreateModels(&CreateModelsOptions{Credentials: credentials})
	models.SetProvider(provider)

	model := types.Model{Id: "m", Provider: "p", Api: types.ApiAnthropicMessages, Headers: map[string]string{"X-Model": "static"}}
	stream := models.Stream(context.Background(), model, types.Context{Messages: []types.Message{}}, &ModelsApiStreamOptions{
		StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: strPtr("explicit-key")}},
	})
	<-stream.Next()
	if captured.apiKey == nil || *captured.apiKey != "explicit-key" {
		t.Fatalf("api key = %v; want explicit-key", captured.apiKey)
	}
	if captured.headers["X-Model"] == nil || *captured.headers["X-Model"] != "static" {
		t.Fatalf("model headers not merged: %v", captured.headers)
	}
}

type authCapture struct {
	apiKey  *string
	headers types.ProviderHeaders
}

type capturingStreams struct {
	capture *authCapture
}

func (c *capturingStreams) Stream(model *types.Model, _ *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	c.capture.apiKey = options.APIKey
	c.capture.headers = options.Headers
	message := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	stream.Push(types.NewDoneEvent(types.StopReasonStop, message))
	return stream
}

func (c *capturingStreams) StreamSimple(model *types.Model, _ *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	message := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	stream.Push(types.NewDoneEvent(types.StopReasonStop, message))
	return stream
}

func (c *capturingStreams) FetchDeferred(*types.Model, types.DeferredHandle, *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	return nil, errors.New("not supported")
}

func (c *capturingStreams) CancelDeferred(*types.Model, types.DeferredHandle, *types.DeferredCancelOptions) error {
	return errors.New("not supported")
}

// TestImagesModels covers image provider registration, auth application and the
// error normalization contract.
func TestImagesModels(t *testing.T) {
	ctx := context.Background()
	model := types.ImagesModel{
		Model:  types.Model{Id: "img", Provider: "images", Api: types.ApiOpenRouterImages},
		Output: []types.ImagesModelOutputModality{types.ImagesModelOutputImage},
	}

	var receivedKey *string
	provider := CreateImagesProvider(CreateImagesProviderOptions{
		ID:     "images",
		Auth:   testAPIKeyAuth(),
		Models: []types.ImagesModel{model},
		API: imagesStreamsFunc(func(_ *types.ImagesModel, _ *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
			receivedKey = options.APIKey
			return &types.AssistantImages{
				Api: types.ImagesApi(model.Api), Provider: types.ImagesProviderId(model.Provider), Model: model.Id,
				Output:     []types.ImagesOutputContent{types.ImageBlock("AAA", "image/png")},
				StopReason: types.ImagesStopReasonStop,
			}, nil
		}),
	})

	images := CreateImagesModels(nil)
	images.SetProvider(provider)
	if got := images.GetModel("images", "img"); got == nil {
		t.Fatal("registered image model not found")
	}

	result := images.GenerateImages(ctx, model, types.ImagesContext{}, &types.ImagesOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: strPtr("explicit")}})
	if result.StopReason != types.ImagesStopReasonStop {
		t.Fatalf("generate result = %+v", result)
	}
	if receivedKey == nil || *receivedKey != "explicit" {
		t.Fatalf("received key = %v; want explicit", receivedKey)
	}

	// A failing provider is normalized into an error result, never a Go error.
	failing := CreateImagesProvider(CreateImagesProviderOptions{
		ID:   "images",
		Auth: testAPIKeyAuth(),
		API: imagesStreamsFunc(func(*types.ImagesModel, *types.ImagesContext, *types.ImagesOptions) (*types.AssistantImages, error) {
			return nil, errors.New("boom")
		}),
	})
	images.SetProvider(failing)
	failed := images.GenerateImages(ctx, model, types.ImagesContext{}, nil)
	if failed.StopReason != types.ImagesStopReasonError || failed.ErrorMessage == nil || *failed.ErrorMessage != "boom" {
		t.Fatalf("failed generate = %+v", failed)
	}
}

type imagesStreamsFunc func(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error)

func (f imagesStreamsFunc) GenerateImages(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	return f(model, context, options)
}

// TestImagesProviderRefreshSharesInflight verifies concurrent refresh calls
// share one in-flight fetch.
func TestImagesProviderRefreshSharesInflight(t *testing.T) {
	var calls int64
	release := make(chan struct{})
	provider := CreateImagesProvider(CreateImagesProviderOptions{
		ID:   "images",
		Auth: testAPIKeyAuth(),
		RefreshModels: func(_ context.Context) ([]types.ImagesModel, error) {
			atomic.AddInt64(&calls, 1)
			<-release
			return []types.ImagesModel{{Model: types.Model{Id: "refreshed", Provider: "images"}}}, nil
		},
	})
	refreshable, ok := provider.(RefreshableImagesProvider)
	if !ok {
		t.Fatal("provider with RefreshModels not refreshable")
	}

	var waitGroup sync.WaitGroup
	for i := 0; i < 4; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := refreshable.RefreshModels(context.Background()); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	waitGroup.Wait()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("refresh fetch calls = %d; want 1", got)
	}
	if got := provider.GetModels(); len(got) != 1 || got[0].Id != "refreshed" {
		t.Fatalf("refreshed models = %+v", got)
	}
}

// TestRadiusCatalogOverlay uses the bundled Radius catalog data to confirm a
// config-derived overlay is published and restored through the model store.
func TestRadiusCatalogOverlay(t *testing.T) {
	ctx := context.Background()
	config := catalog.RadiusGatewayConfig{
		BaseUrl: "https://radius.example/v1",
		Models: []catalog.RadiusGatewayModel{
			{Id: "balanced", Name: "Fresh Balanced", Input: []types.ModelInputModality{types.ModelInputText}, ContextWindow: 424242, MaxTokens: 32000},
			{Id: "organization-only", Name: "Organization Only", Input: []types.ModelInputModality{types.ModelInputText}, ContextWindow: 128000, MaxTokens: 16000},
		},
	}
	overlay := catalog.GetRadiusModelsFromConfig("radius", config)
	if len(overlay) != 2 {
		t.Fatalf("radius overlay models = %d; want 2", len(overlay))
	}

	provider := CreateProvider(CreateProviderOptions{
		ID:     "radius",
		Auth:   testAPIKeyAuth(),
		Models: []types.Model{{Id: "public", Provider: "radius"}},
		FetchModels: func(_ context.Context, _ *RefreshModelsContext) ([]types.Model, error) {
			return overlay, nil
		},
	})
	store := NewInMemoryModelsStore()
	models := CreateModels(&CreateModelsOptions{ModelsStore: store})
	models.SetProvider(provider)
	if _, err := models.Refresh(ctx, &ModelsRefreshOptions{Providers: []string{"radius"}}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	balanced := models.GetModel("radius", "balanced")
	if balanced == nil || balanced.ContextWindow != 424242 {
		t.Fatalf("radius balanced = %+v", balanced)
	}
	if models.GetModel("radius", "public") == nil {
		t.Fatal("static public catalog lost after overlay")
	}
}
