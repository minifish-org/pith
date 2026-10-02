// Pi 1.0 provider protocol regression self-tests.
//
// These tests port the upstream Mistral reasoning-mode and stream-shape
// regressions into fully offline HTTP fixtures. They cover:
//
//   - empty string and empty typed-text content deltas that must not open or
//     split a block around thinking content,
//   - reasoning mode selection from the model's thinkingLevelMap (mapped
//     effort, off mapping, unsupported-level clamp and "high" fallback) with
//     no name-based heuristics,
//   - prompt_mode selection for reasoning models without a level map,
//   - prompt cache key / retention handling.
//
// Every fixture uses httptest servers or payload capture and dummy keys; no
// live provider request is made.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

func piV1MistralLevelMap(entries map[types.ModelThinkingLevel]*string) types.ThinkingLevelMap {
	levelMap := types.ThinkingLevelMap{}
	for level, value := range entries {
		levelMap[level] = value
	}
	return levelMap
}

func piV1MistralModel(id string, reasoning bool, levelMap types.ThinkingLevelMap, baseURL string) types.Model {
	model := types.Model{
		Id:            id,
		Name:          id,
		Api:           types.ApiMistralConversations,
		Provider:      types.ProviderId("mistral"),
		BaseUrl:       baseURL,
		Reasoning:     reasoning,
		Input:         []types.ModelInputModality{types.ModelInputText},
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
	if levelMap != nil {
		model.ThinkingLevelMap = levelMap
	}
	return model
}

// capturePiV1MistralPayload runs the simple Mistral stream against an
// unreachable base URL and records the payload that would be sent. The request
// failure after OnPayload is irrelevant: the payload is captured before any
// network activity, matching the upstream regression helper.
func capturePiV1MistralPayload(t *testing.T, model types.Model, options *types.SimpleStreamOptions) map[string]any {
	t.Helper()
	var captured map[string]any
	options.OnPayload = func(payload any, _ *types.Model) (any, error) {
		if record, ok := payload.(map[string]any); ok {
			captured = record
		}
		return payload, nil
	}
	stream := MistralConversationsStreamSimple(&model, mistralTestTranscript(), options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The stream terminates with an error because the base URL is unreachable;
	// the captured payload is what this test asserts on.
	_, _ = stream.Result(ctx)
	if captured == nil {
		t.Fatal("expected the request payload to be captured before the request failed")
	}
	return captured
}

func piV1MistralSimpleOptions(apiKey string) *types.SimpleStreamOptions {
	return &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey}}}
}

func piV1Str(value string) *string { return &value }

func TestPiV1MistralIgnoresEmptyStringAndTypedTextDeltas(t *testing.T) {
	body := ""
	for _, delta := range []string{
		`{"content":""}`,
		`{"content":[{"type":"thinking","thinking":[{"type":"text","text":"one"}]}]}`,
		`{"content":""}`,
		`{"content":[{"type":"text","text":""}]}`,
		`{"content":[{"type":"thinking","thinking":[{"type":"text","text":" two"}]},{"type":"text","text":"answer"}]}`,
	} {
		body += fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
	}
	body += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	model := piV1MistralModel("non-whitelisted", true, nil, server.URL)
	events, result := drainAssistantStream(t, MistralConversationsStream(&model, mistralTestTranscript(), &MistralOptions{StreamOptions: testStreamOptions("fixture-key", nil)}))

	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if len(result.Content) != 2 {
		t.Fatalf("empty deltas opened or split blocks: %s", piV1JSON(result.Content))
	}
	if result.Content[0].Type != types.ContentTypeThinking || result.Content[0].Thinking == nil || result.Content[0].Thinking.Thinking != "one two" {
		t.Fatalf("thinking block lost content: %s", piV1JSON(result.Content))
	}
	if result.Content[1].Type != types.ContentTypeText || result.Content[1].Text == nil || result.Content[1].Text.Text != "answer" {
		t.Fatalf("text block lost content: %s", piV1JSON(result.Content))
	}
	want := []string{"start", "thinking_start", "thinking_delta", "thinking_delta", "thinking_end", "text_start", "text_delta", "text_end", "done"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestPiV1MistralUsesMappedEffortFromThinkingLevelMap(t *testing.T) {
	levelMap := piV1MistralLevelMap(map[types.ModelThinkingLevel]*string{
		types.ThinkingHigh: piV1Str("medium"),
	})
	model := piV1MistralModel("non-whitelisted", true, levelMap, "http://127.0.0.1:9")

	options := piV1MistralSimpleOptions("fake-key")
	reasoning := types.ThinkingHigh
	options.Reasoning = &reasoning
	payload := capturePiV1MistralPayload(t, model, options)

	if payload["reasoningEffort"] != "medium" {
		t.Fatalf("model's mapped effort ignored: %v", payload)
	}
	if _, present := payload["promptMode"]; present {
		t.Fatalf("prompt_mode must not be sent when a thinking level map exists: %v", payload)
	}
}

func TestPiV1MistralClampsUnsupportedLevelToMappedEffort(t *testing.T) {
	levelMap := piV1MistralLevelMap(map[types.ModelThinkingLevel]*string{
		types.ThinkingOff:     piV1Str("none"),
		types.ThinkingMinimal: nil,
		types.ThinkingLow:     nil,
		types.ThinkingMedium:  nil,
		types.ThinkingHigh:    piV1Str("high"),
	})
	model := piV1MistralModel("non-whitelisted", true, levelMap, "http://127.0.0.1:9")

	options := piV1MistralSimpleOptions("fake-key")
	reasoning := types.ThinkingLow
	options.Reasoning = &reasoning
	payload := capturePiV1MistralPayload(t, model, options)

	if payload["reasoningEffort"] != "high" {
		t.Fatalf("unsupported level must clamp to a supported mapped effort: %v", payload)
	}
}

func TestPiV1MistralOffUsesOffMapping(t *testing.T) {
	levelMap := piV1MistralLevelMap(map[types.ModelThinkingLevel]*string{
		types.ThinkingOff:  piV1Str("none"),
		types.ThinkingHigh: piV1Str("high"),
	})
	model := piV1MistralModel("non-whitelisted", true, levelMap, "http://127.0.0.1:9")

	payload := capturePiV1MistralPayload(t, model, piV1MistralSimpleOptions("fake-key"))

	if payload["reasoningEffort"] != "none" {
		t.Fatalf("off must use its off mapping: %v", payload)
	}
	if _, present := payload["promptMode"]; present {
		t.Fatalf("prompt_mode must not be sent when a thinking level map exists: %v", payload)
	}
}

func TestPiV1MistralPromptModeWithoutThinkingLevelMap(t *testing.T) {
	for _, id := range []string{"synthetic-model-name", "magistral-medium-latest", "mistral-medium-latest"} {
		model := piV1MistralModel(id, true, nil, "http://127.0.0.1:9")
		options := piV1MistralSimpleOptions("fake-key")
		reasoning := types.ThinkingMedium
		options.Reasoning = &reasoning
		payload := capturePiV1MistralPayload(t, model, options)

		if payload["promptMode"] != "reasoning" {
			t.Fatalf("%s: reasoning models without a level map use prompt_mode: %v", id, payload)
		}
		if _, present := payload["reasoningEffort"]; present {
			t.Fatalf("%s: prompt_mode models must not send reasoning_effort: %v", id, payload)
		}
	}
}

// TestPiV1MistralEffortSelectionIgnoresModelName guards against the removed
// name-based heuristic: the map, not the identifier, selects reasoning_effort.
func TestPiV1MistralEffortSelectionIgnoresModelName(t *testing.T) {
	levelMap := piV1MistralLevelMap(map[types.ModelThinkingLevel]*string{
		types.ThinkingOff:  piV1Str("none"),
		types.ThinkingHigh: piV1Str("high"),
	})
	withMap := piV1MistralModel("some-random-name", true, levelMap, "http://127.0.0.1:9")
	options := piV1MistralSimpleOptions("fake-key")
	reasoning := types.ThinkingHigh
	options.Reasoning = &reasoning
	payload := capturePiV1MistralPayload(t, withMap, options)
	if payload["reasoningEffort"] != "high" || payload["promptMode"] != nil {
		t.Fatalf("mapped model must use reasoning_effort: %v", payload)
	}

	withoutMap := piV1MistralModel("mistral-small-2603", true, nil, "http://127.0.0.1:9")
	options = piV1MistralSimpleOptions("fake-key")
	options.Reasoning = &reasoning
	payload = capturePiV1MistralPayload(t, withoutMap, options)
	if payload["promptMode"] != "reasoning" {
		t.Fatalf("unmapped model must use prompt_mode regardless of name: %v", payload)
	}
}

func TestPiV1MistralNonReasoningOmitsReasoningControls(t *testing.T) {
	model := piV1MistralModel("non-reasoning", false, nil, "http://127.0.0.1:9")
	options := piV1MistralSimpleOptions("fake-key")
	reasoning := types.ThinkingMedium
	options.Reasoning = &reasoning
	payload := capturePiV1MistralPayload(t, model, options)

	if _, present := payload["reasoningEffort"]; present {
		t.Fatalf("non-reasoning model must not send reasoning_effort: %v", payload)
	}
	if _, present := payload["promptMode"]; present {
		t.Fatalf("non-reasoning model must not send prompt_mode: %v", payload)
	}
}

func TestPiV1MistralPromptCacheKeyRetention(t *testing.T) {
	session := "session-123"
	model := piV1MistralModel("non-reasoning", false, nil, "http://127.0.0.1:9")

	options := piV1MistralSimpleOptions("fake-key")
	options.SessionId = &session
	payload := capturePiV1MistralPayload(t, model, options)
	if payload["promptCacheKey"] != session {
		t.Fatalf("session id must be forwarded as prompt_cache_key: %v", payload)
	}

	none := types.CacheRetentionNone
	options = piV1MistralSimpleOptions("fake-key")
	options.SessionId = &session
	options.CacheRetention = &none
	payload = capturePiV1MistralPayload(t, model, options)
	if _, present := payload["promptCacheKey"]; present {
		t.Fatalf("cache retention none must omit prompt_cache_key: %v", payload)
	}
}

func piV1JSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
