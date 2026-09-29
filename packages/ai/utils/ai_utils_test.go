package utils

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

// This file ports the deterministic scenarios of the upstream ai-utils test
// suite (context-estimate, error-body, node-http-proxy, provider-retry, retry,
// system-message-replay, uuid) at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe. Live-credential and wall-clock
// scenarios become deterministic local tests with fixed inputs.

func TestParseStreamingJSONToleratesIncompleteInput(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "{}"},
		{"{", "{}"},
		{`{"name":"hello`, `{"name":"hello"}`},
		{`{"items":[1,2`, `{"items":[1,2]}`},
		{`{"n":false,"v":null}`, `{"n":false,"v":null}`},
	}
	for _, testCase := range cases {
		encoded, err := json.Marshal(ParseStreamingJSON(testCase.input))
		if err != nil {
			t.Fatalf("%q: %v", testCase.input, err)
		}
		if string(encoded) != testCase.want {
			t.Fatalf("%q: want %s, got %s", testCase.input, testCase.want, encoded)
		}
	}
}

func TestParseJSONWithRepair(t *testing.T) {
	value, err := ParseJSONWithRepair(`{"x":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if object, ok := value.(map[string]any); !ok || object["x"] == nil {
		t.Fatalf("unexpected value: %#v", value)
	}
	if _, err := ParseJSONWithRepair("{x:}"); err == nil {
		t.Fatal("expected an unrepairable object to fail")
	}
	value, err = ParseJSONWithRepair("[1,2]")
	if err != nil {
		t.Fatal(err)
	}
	if array, ok := value.([]any); !ok || len(array) != 2 {
		t.Fatalf("unexpected array: %#v", value)
	}
}

func TestRepairJSONEscapesControlCharacters(t *testing.T) {
	repaired := RepairJSON("\"a\nb\"")
	if repaired != `"a\nb"` {
		t.Fatalf("unexpected repair: %q", repaired)
	}
}

func TestContentTextJoinsTextBlocks(t *testing.T) {
	if got := ContentText(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
	blocks := []types.ContentBlock{
		types.TextBlock("a"),
		types.ThinkingBlock("secret"),
		types.TextBlock("b"),
	}
	if got := ContentText(blocks); got != "a\nb" {
		t.Fatalf("unexpected join: %q", got)
	}
	if got := ContentText([]types.ContentBlock{types.TextBlock("中"), types.TextBlock("文")}, "|"); got != "中|文" {
		t.Fatalf("unexpected separator join: %q", got)
	}
}

func TestEstimateTextTokensUsesUTF16Length(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"", 0},
		{"abc", 1},
		{"😀中文", 1},
		{strings.Repeat("a", 41), 11},
	}
	for _, testCase := range cases {
		if got := EstimateTextTokens(testCase.text); got != testCase.want {
			t.Fatalf("%q: want %v, got %v", testCase.text, testCase.want, got)
		}
	}
}

func TestCalculateContextTokens(t *testing.T) {
	usage := types.Usage{Input: 10, Output: 3, CacheRead: 2, CacheWrite: 1, TotalTokens: 16}
	if got := CalculateContextTokens(usage); got != 16 {
		t.Fatalf("expected 16, got %v", got)
	}
	usage.TotalTokens = 0
	if got := CalculateContextTokens(usage); got != 16 {
		t.Fatalf("expected the summed fallback, got %v", got)
	}
}

func TestEstimateContextTokensIgnoresStaleUsage(t *testing.T) {
	stale := types.NewAssistantMessage(types.ApiOpenAIResponses, types.ProviderOpenAI, "test-model", 100)
	stale.Content = []types.ContentBlock{types.TextBlock("kept")}
	stale.StopReason = types.StopReasonStop
	stale.Usage = types.Usage{Input: 9500, TotalTokens: 9500}

	systemPrompt := "system"
	messages := []types.Message{
		types.NewUserMessageVariant(types.NewUserMessage("summary", 200)),
		types.NewAssistantMessageVariant(stale),
		types.NewUserMessageVariant(types.NewUserMessage(strings.Repeat("x", 4000), 300)),
	}
	estimate := EstimateContextTokens(NormalizeContext(types.Context{SystemPrompt: &systemPrompt, Messages: messages}))
	if estimate.UsageTokens != 0 {
		t.Fatalf("expected stale usage to be ignored, got %v", estimate.UsageTokens)
	}
	if estimate.LastUsageIndex != nil {
		t.Fatalf("expected no last usage index, got %v", *estimate.LastUsageIndex)
	}
	if estimate.TrailingTokens != estimate.Tokens {
		t.Fatalf("expected trailing tokens to equal the total, got %+v", estimate)
	}
	if estimate.Tokens != 1005 {
		t.Fatalf("expected 1005 estimate, got %v", estimate.Tokens)
	}
}

func TestEstimateContextTokensUsesFreshUsage(t *testing.T) {
	fresh := types.NewAssistantMessage(types.ApiOpenAIResponses, types.ProviderOpenAI, "test-model", 400)
	fresh.Content = []types.ContentBlock{types.TextBlock("kept")}
	fresh.StopReason = types.StopReasonStop
	fresh.Usage = types.Usage{Input: 2000, TotalTokens: 2000}

	messages := []types.Message{
		types.NewUserMessageVariant(types.NewUserMessage("summary", 200)),
		types.NewAssistantMessageVariant(fresh),
		types.NewUserMessageVariant(types.NewUserMessage("tail", 500)),
	}
	estimate := EstimateContextTokens(messages)
	if estimate.UsageTokens != 2000 {
		t.Fatalf("expected fresh usage, got %v", estimate.UsageTokens)
	}
	if estimate.LastUsageIndex == nil || *estimate.LastUsageIndex != 1 {
		t.Fatalf("unexpected last usage index: %+v", estimate.LastUsageIndex)
	}
}

func TestRetryDelayMsCapsDelay(t *testing.T) {
	limit := 350.0
	policy := RetryPolicy{Enabled: true, BaseDelayMs: 100, MaxAgentDelayMs: &limit}
	if got := RetryDelayMs(policy, 1); got != 100 {
		t.Fatalf("expected 100, got %v", got)
	}
	if got := RetryDelayMs(policy, 3); got != 350 {
		t.Fatalf("expected the cap 350, got %v", got)
	}
	zero := 0.0
	if got := RetryDelayMs(RetryPolicy{BaseDelayMs: 0, MaxAgentDelayMs: &zero}, 8); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
	if got := RetryDelayMs(RetryPolicy{BaseDelayMs: 2000}, 6); got != DefaultMaxAgentRetryDelayMs {
		t.Fatalf("expected the default cap, got %v", got)
	}
}

func fakeAssistant(stopReason types.StopReason, errorMessage string) types.AssistantMessage {
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderOpenAI, "m", 1)
	message.StopReason = stopReason
	if errorMessage != "" {
		value := errorMessage
		message.ErrorMessage = &value
	}
	return message
}

func TestIsRetryableAssistantError(t *testing.T) {
	retryable := []string{
		"overloaded_error",
		"520 status code (no body)",
		"524 status code (no body)",
		"The socket connection was closed unexpectedly.",
		"OpenAI Responses stream ended before a terminal response event",
		"getaddrinfo ENOTFOUND api.example.com",
		"ResourceExhausted: Worker local total request limit reached",
		"You can retry your request",
		"Error: exceeded request buffer limit while retrying upstream",
	}
	for _, message := range retryable {
		if !IsRetryableAssistantError(fakeAssistant(types.StopReasonError, message)) {
			t.Fatalf("expected %q to be retryable", message)
		}
	}
	nonRetryable := []string{
		"429 quota exceeded",
		"insufficient_quota",
		"GoUsageLimitError",
	}
	for _, message := range nonRetryable {
		if IsRetryableAssistantError(fakeAssistant(types.StopReasonError, message)) {
			t.Fatalf("expected %q to be non-retryable", message)
		}
	}
	if IsRetryableAssistantError(fakeAssistant(types.StopReasonStop, "terminated")) {
		t.Fatal("expected a successful message to be non-retryable")
	}
}

func TestRetryAssistantCallStopsOnSuccess(t *testing.T) {
	calls := 0
	produce := func() (types.AssistantMessage, error) {
		calls++
		return fakeAssistant(types.StopReasonStop, ""), nil
	}
	policy := &RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 0}
	if _, err := RetryAssistantCall(produce, policy, context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one call, got %d", calls)
	}
}

func TestRetryAssistantCallDoesNotRetryAborted(t *testing.T) {
	calls := 0
	produce := func() (types.AssistantMessage, error) {
		calls++
		return fakeAssistant(types.StopReasonAborted, ""), nil
	}
	policy := &RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 0}
	result, err := RetryAssistantCall(produce, policy, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("unexpected stop reason: %s", result.StopReason)
	}
	if calls != 1 {
		t.Fatalf("expected no retry, got %d calls", calls)
	}
}

func TestRetryAssistantCallDoesNotRetryQuotaError(t *testing.T) {
	calls := 0
	produce := func() (types.AssistantMessage, error) {
		calls++
		return fakeAssistant(types.StopReasonError, "insufficient_quota"), nil
	}
	policy := &RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 0}
	scheduled := 0
	callbacks := &RetryCallbacks{OnRetryScheduled: func(int, int, float64, string) error { scheduled++; return nil }}
	result, err := RetryAssistantCall(produce, policy, context.Background(), callbacks)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("unexpected stop reason: %s", result.StopReason)
	}
	if calls != 1 || scheduled != 0 {
		t.Fatalf("expected no retry, got %d calls and %d schedules", calls, scheduled)
	}
}

func TestRetryAssistantCallExhaustsRetries(t *testing.T) {
	calls := 0
	produce := func() (types.AssistantMessage, error) {
		calls++
		return fakeAssistant(types.StopReasonError, "terminated"), nil
	}
	policy := &RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 0}
	scheduled := 0
	finishedCalls := 0
	callbacks := &RetryCallbacks{
		OnRetryScheduled: func(attempt, maxAttempts int, delayMs float64, message string) error {
			scheduled++
			return nil
		},
		OnRetryFinished: func(success bool, attempt int, finalError string) error {
			finishedCalls++
			if success || attempt != 3 || finalError != "terminated" {
				t.Errorf("unexpected finish callback: %v %d %q", success, attempt, finalError)
			}
			return nil
		},
	}
	result, err := RetryAssistantCall(produce, policy, context.Background(), callbacks)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("unexpected stop reason: %s", result.StopReason)
	}
	if calls != 4 {
		t.Fatalf("expected 4 calls (1 initial + 3 retries), got %d", calls)
	}
	if scheduled != 3 {
		t.Fatalf("expected 3 schedules, got %d", scheduled)
	}
	if finishedCalls != 1 {
		t.Fatalf("expected one finish, got %d", finishedCalls)
	}
}

func TestRetryAssistantCallDisabledPolicy(t *testing.T) {
	calls := 0
	produce := func() (types.AssistantMessage, error) {
		calls++
		return fakeAssistant(types.StopReasonError, "terminated"), nil
	}
	policy := &RetryPolicy{Enabled: false, MaxRetries: 3, BaseDelayMs: 0}
	if _, err := RetryAssistantCall(produce, policy, context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected a single call, got %d", calls)
	}
}

type providerFieldsError struct {
	message string
	fields  map[string]any
}

func (e *providerFieldsError) Error() string { return e.message }

func (e *providerFieldsError) ProviderErrorFields() map[string]any { return e.fields }

func TestNormalizeProviderErrorExtractsStatusAndBody(t *testing.T) {
	mistral := &providerFieldsError{message: "Mistral request failed", fields: map[string]any{
		"statusCode": 403,
		"body":       `{"error":"blocked by gateway WAF"}`,
	}}
	normalized := NormalizeProviderError(mistral)
	if normalized.Status == nil || *normalized.Status != 403 {
		t.Fatalf("expected status 403, got %v", normalized.Status)
	}
	if normalized.Body == nil || *normalized.Body != `{"error":"blocked by gateway WAF"}` {
		t.Fatalf("unexpected body: %v", normalized.Body)
	}
	if normalized.MessageCarriesBody {
		t.Fatal("expected the message not to carry the body")
	}

	empty := &providerFieldsError{message: "403 status code (no body)", fields: map[string]any{
		"status": 403,
		"error":  map[string]any{},
	}}
	normalized = NormalizeProviderError(empty)
	if normalized.Body != nil {
		t.Fatalf("expected an empty parsed body to be ignored, got %v", *normalized.Body)
	}
	if !normalized.MessageCarriesBody {
		t.Fatal("expected messageCarriesBody when there is no body")
	}

	plain := &providerFieldsError{message: "400 status code (no body)", fields: map[string]any{
		"status": 400,
		"error":  map[string]any{"message": "schema validation failed"},
	}}
	normalized = NormalizeProviderError(plain)
	if normalized.Body == nil || *normalized.Body != `{"message":"schema validation failed"}` {
		t.Fatalf("unexpected body: %v", normalized.Body)
	}

	nonError := NormalizeProviderErrorValue(map[string]any{"reason": "boom"})
	if nonError.Message != `{"reason":"boom"}` {
		t.Fatalf("unexpected non-error message: %q", nonError.Message)
	}
}

func TestFormatProviderError(t *testing.T) {
	normalized := NormalizeProviderError(&providerFieldsError{message: "403 status code (no body)", fields: map[string]any{
		"status": 403,
		"error":  map[string]any{"error": "blocked by gateway WAF"},
	}})
	formatted := FormatProviderError(normalized, nil)
	if !strings.Contains(formatted, "403") || !strings.Contains(formatted, "blocked by gateway WAF") {
		t.Fatalf("unexpected format: %q", formatted)
	}
	prefix := "OpenAI API error"
	formatted = FormatProviderError(normalized, &prefix)
	if formatted != `OpenAI API error (403): {"error":"blocked by gateway WAF"}` {
		t.Fatalf("unexpected prefixed format: %q", formatted)
	}
}

func TestTruncateErrorText(t *testing.T) {
	long := strings.Repeat("x", MaxProviderErrorBodyChars+50)
	truncated := TruncateErrorText(long, MaxProviderErrorBodyChars)
	if !strings.Contains(truncated, "... [truncated 50 chars]") {
		t.Fatalf("unexpected truncation note: %q", truncated[len(truncated)-40:])
	}
	if len(truncated) >= len(long) {
		t.Fatal("expected the truncated text to be shorter")
	}
	if TruncateErrorText("short", 100) != "short" {
		t.Fatal("expected short text to pass through")
	}
}

func TestResolveHttpProxyUrlForTarget(t *testing.T) {
	env := types.ProviderEnv{"https_proxy": stringPointer("http://proxy.local:8080"), "no_proxy": stringPointer("example.com")}
	proxy, err := ResolveHttpProxyUrlForTarget("https://api.openai.com/v1", env)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.Host != "proxy.local:8080" {
		t.Fatalf("unexpected proxy: %v", proxy)
	}
	// Host listed in no_proxy is not proxied.
	proxy, err = ResolveHttpProxyUrlForTarget("https://example.com/x", env)
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil {
		t.Fatalf("expected no proxy for excluded host, got %v", proxy)
	}
	// no_proxy=* disables proxying entirely.
	wildcard := types.ProviderEnv{"https_proxy": stringPointer("http://proxy.local:8080"), "no_proxy": stringPointer("*")}
	proxy, err = ResolveHttpProxyUrlForTarget("https://api.openai.com/v1", wildcard)
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil {
		t.Fatalf("expected no proxy for wildcard no_proxy, got %v", proxy)
	}
	// SOCKS proxies are rejected.
	socks := types.ProviderEnv{"https_proxy": stringPointer("socks5://proxy.local:1080")}
	if _, err := ResolveHttpProxyUrlForTarget("https://api.openai.com/v1", socks); err == nil {
		t.Fatal("expected a SOCKS proxy to be rejected")
	}
}

func stringPointer(value string) *string { return &value }

func TestSanitizeSurrogatesRemovesUnpaired(t *testing.T) {
	paired := "Hello \U0001F648 World"
	if got := SanitizeSurrogates(paired); got != paired {
		t.Fatalf("expected paired emoji to survive, got %q", got)
	}
	unpaired := string([]byte{0xED, 0xA0, 0xBD})
	if got := SanitizeSurrogates("Text " + unpaired + " here"); got != "Text  here" {
		t.Fatalf("expected the unpaired surrogate to be removed, got %q", got)
	}
}

func TestShortHashIsDeterministic(t *testing.T) {
	if ShortHash("abc") != ShortHash("abc") {
		t.Fatal("expected a deterministic hash")
	}
	if ShortHash("abc") == ShortHash("abd") {
		t.Fatal("expected different inputs to hash differently")
	}
}

func TestHeadersToRecord(t *testing.T) {
	record := ProviderHeadersToRecord(types.ProviderHeaders{"x-a": stringPointer("1"), "x-b": nil})
	if len(record) != 1 || record["x-a"] != "1" {
		t.Fatalf("unexpected record: %v", record)
	}
	if ProviderHeadersToRecord(types.ProviderHeaders{"x-b": nil}) != nil {
		t.Fatal("expected nil when every header is suppressed")
	}
}

func TestUUIDv7RejectsInvalidTimestamp(t *testing.T) {
	invalid := []float64{-1, 1.5, 281474976710656}
	for _, value := range invalid {
		if _, err := UUIDv7(&value); err == nil {
			t.Fatalf("expected timestamp %v to be rejected", value)
		}
	}
	fixed := float64(0x0123456789ab)
	first, err := UUIDv7(&fixed)
	if err != nil {
		t.Fatal(err)
	}
	second, err := UUIDv7(&fixed)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected distinct tails")
	}
	if first[14] != '7' {
		t.Fatalf("expected a version-7 uuid, got %q", first)
	}
}

func TestIsContextOverflow(t *testing.T) {
	overflow := fakeAssistant(types.StopReasonError, "prompt is too long: 213462 tokens > 200000 maximum")
	if !IsContextOverflow(overflow, nil) {
		t.Fatal("expected an Anthropic overflow message to be detected")
	}
	throttled := fakeAssistant(types.StopReasonError, "Throttling error: Too many tokens, please wait before trying again.")
	if IsContextOverflow(throttled, nil) {
		t.Fatal("expected a throttling error not to be treated as overflow")
	}
	limit := 100000.0
	silent := fakeAssistant(types.StopReasonStop, "")
	silent.Usage = types.Usage{Input: 200000, TotalTokens: 200000}
	if !IsContextOverflow(silent, &limit) {
		t.Fatal("expected silent overflow to be detected")
	}
}

func TestValidateToolArgumentsCoercesAndValidates(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)
	tool := types.NewTool("echo", "", schema)

	coerced, err := ValidateToolArguments(tool, types.NewToolCall("c", "echo", json.RawMessage(`{"n":"4"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if object, ok := coerced.(map[string]any); !ok || toInt(object["n"]) != 4 {
		t.Fatalf("expected coercion to 4, got %#v", coerced)
	}

	coerced, err = ValidateToolArguments(tool, types.NewToolCall("c", "echo", json.RawMessage(`{"n":null}`)))
	if err != nil {
		t.Fatal(err)
	}
	if object, ok := coerced.(map[string]any); !ok || toInt(object["n"]) != 0 {
		t.Fatalf("expected null to coerce to 0, got %#v", coerced)
	}

	if _, err := ValidateToolArguments(tool, types.NewToolCall("c", "echo", json.RawMessage(`{"n":"nope"}`))); err == nil {
		t.Fatal("expected a non-integer to fail validation")
	}
}

func toInt(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		parsed, _ := v.Int64()
		return parsed
	default:
		return -1
	}
}

func TestGetCurrentSystemPromptReplaysMessages(t *testing.T) {
	messages := []types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("first", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hi", 2)),
		types.NewSystemMessageVariant(types.NewSystemMessage("second", 3)),
	}
	if got := GetCurrentSystemPrompt(messages); got != "first\n\nsecond" {
		t.Fatalf("unexpected prompt: %q", got)
	}
	if got := GetCurrentSystemPrompt(nil); got != "" {
		t.Fatalf("expected empty prompt, got %q", got)
	}
}

func TestGetCurrentToolsAppliesDeltas(t *testing.T) {
	first := types.NewSystemMessage("", 1)
	first.ToolsAdded = []types.Tool{types.NewTool("a", "A", json.RawMessage(`{"type":"object"}`))}
	second := types.NewSystemMessage("", 2)
	second.ToolsRemoved = []types.ToolReference{{Name: "a"}}
	second.ToolsAdded = []types.Tool{types.NewTool("b", "B", json.RawMessage(`{"type":"object"}`))}
	messages := []types.Message{types.NewSystemMessageVariant(first), types.NewSystemMessageVariant(second)}
	tools := GetCurrentTools(messages)
	if len(tools) != 1 || tools[0].Name != "b" {
		t.Fatalf("expected only the re-added tool b, got %+v", tools)
	}
}

func TestCollapseSystemMessages(t *testing.T) {
	messages := []types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("first", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hi", 2)),
		types.NewSystemMessageVariant(types.NewSystemMessage("second", 3)),
	}
	collapsed := CollapseSystemMessages(types.TranscriptContext{Messages: messages})
	if len(collapsed.Messages) != 2 {
		t.Fatalf("expected a leading system message and the user message, got %d", len(collapsed.Messages))
	}
	if collapsed.Messages[0].Role != types.SystemMessageRole || collapsed.Messages[1].Role != types.UserMessageRole {
		t.Fatalf("unexpected order: %+v", collapsed.Messages)
	}
}

func TestAssistantMessageFrameRoundTrip(t *testing.T) {
	encoder := NewAssistantMessageFrameEncoder()
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderOpenAI, "m", 1)
	message.Content = []types.ContentBlock{}
	start := types.NewStartEvent(message)
	first, err := encoder.Encode(start)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.Type != FrameStart {
		t.Fatalf("unexpected start frame: %+v", first)
	}

	message.Content = append(message.Content, types.TextBlock(""))
	textStart := types.NewTextStartEvent(0, message)
	if _, err := encoder.Encode(textStart); err != nil {
		t.Fatal(err)
	}
	textDelta := types.NewTextDeltaEvent(0, "hello", message)
	if _, err := encoder.Encode(textDelta); err != nil {
		t.Fatal(err)
	}
	textEnd := types.NewTextEndEvent(0, "hello", message)
	frame, err := encoder.Encode(textEnd)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != FrameTextEnd || frame.Content == nil || *frame.Content != "hello" {
		t.Fatalf("unexpected text end frame: %+v", frame)
	}
	if _, err := encoder.Encode(types.NewDoneEvent(types.StopReasonStop, message)); err != nil {
		t.Fatal(err)
	}

	// Reduce the frames back into a message.
	startPartial := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderOpenAI, "m", 1)
	startFrame := AssistantMessageFrame{Type: FrameStart, Partial: &startPartial}
	contentIndex := 0
	text := "hello"
	frames := []AssistantMessageFrame{
		startFrame,
		{Type: FrameTextStart, ContentIndex: &contentIndex, Content: &text},
		{Type: FrameTextEnd, ContentIndex: &contentIndex, Content: &text},
	}
	reduced, err := ReduceAssistantMessageFrames(frames)
	if err != nil {
		t.Fatal(err)
	}
	if reduced == nil || len(reduced.Content) != 1 || reduced.Content[0].Text == nil || reduced.Content[0].Text.Text != "hello" {
		t.Fatalf("unexpected reduced message: %+v", reduced)
	}
}

func TestSleepRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, 1000); err == nil {
		t.Fatal("expected a cancelled sleep to fail")
	}
	if err := Sleep(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
}

func TestGetProviderEnvValuePrefersScopedOverride(t *testing.T) {
	env := types.ProviderEnv{"PITH_TEST_KEY": stringPointer("scoped")}
	if got := GetProviderEnvValue("PITH_TEST_KEY", env); got == nil || *got != "scoped" {
		t.Fatalf("unexpected scoped value: %v", got)
	}
	if got := GetProviderEnvValue("PITH_TEST_ABSENT_KEY_XYZ", nil); got != nil {
		t.Fatalf("expected no value, got %v", *got)
	}
}

func TestRetryProviderRequestRetriesTransient(t *testing.T) {
	attempts := 0
	request := func() (string, error) {
		attempts++
		if attempts < 2 {
			status := 500
			return "", &ProviderError{Err: context.DeadlineExceeded, Status: &status}
		}
		return "ok", nil
	}
	maxRetries := 2
	maxDelay := 0.0
	result, err := RetryProviderRequest(context.Background(), request, ProviderRetryOptions{MaxRetries: &maxRetries, MaxRetryDelayMs: &maxDelay})
	if err != nil {
		t.Fatal(err)
	}
	if result != "ok" || attempts != 2 {
		t.Fatalf("unexpected result %q with %d attempts", result, attempts)
	}
}

func TestRetryProviderRequestHonorsAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := func() (string, error) {
		status := 500
		return "", &ProviderError{Err: context.Canceled, Status: &status}
	}
	if _, err := RetryProviderRequest(ctx, request, ProviderRetryOptions{}); err == nil {
		t.Fatal("expected an aborted request")
	}
}

func TestDiagnosticsExtractThrownValue(t *testing.T) {
	info := ExtractDiagnosticError(context.DeadlineExceeded)
	if !strings.Contains(info.Message, "deadline") {
		t.Fatalf("unexpected diagnostic message: %q", info.Message)
	}
	if FormatThrownValue("boom") != "boom" {
		t.Fatal("expected a plain string to format as itself")
	}
}
