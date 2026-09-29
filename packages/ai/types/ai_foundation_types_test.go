package types

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Mirrors packages/ai/test/message-types.test.ts: JSON-compatible tool result
// details round trip and unknown roles stay recognizable.
func TestToolResultMessageJSONDetails(t *testing.T) {
	base := NewToolResultMessage("call-1", "read", []ContentBlock{}, false, 1)

	objectDetails := base
	objectDetails.Details = json.RawMessage(`{"path":"a.ts","items":["first"],"summary":{"lines":3}}`)
	arrayDetails := base
	arrayDetails.Details = json.RawMessage(`["a","b"]`)
	primitiveDetails := base
	primitiveDetails.Details = json.RawMessage(`"diagnostic"`)
	nullDetails := base
	nullDetails.Details = json.RawMessage(`null`)

	raw, err := json.Marshal(objectDetails)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["role"] != "toolResult" || decoded["toolCallId"] != "call-1" || decoded["isError"] != false {
		t.Fatalf("unexpected tool result fields: %v", decoded)
	}
	if decoded["timestamp"].(float64) != 1 {
		t.Fatalf("timestamp = %v; want 1", decoded["timestamp"])
	}
	details, ok := decoded["details"].(map[string]any)
	if !ok || details["path"] != "a.ts" {
		t.Fatalf("details = %v; want object with path", decoded["details"])
	}
	summary := details["summary"].(map[string]any)
	if summary["lines"].(float64) != 3 {
		t.Fatalf("summary = %v; want lines 3", summary)
	}

	for name, message := range map[string]ToolResultMessage{
		"array":     arrayDetails,
		"primitive": primitiveDetails,
		"null":      nullDetails,
	} {
		roundTripped, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got ToolResultMessage
		if err := json.Unmarshal(roundTripped, &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got.Details) != string(message.Details) {
			t.Fatalf("%s: details = %s; want %s", name, got.Details, message.Details)
		}
	}

	// A tool result message is assignable to the transcript message union and
	// keeps the same detail payload.
	message := NewToolResultMessageVariant(objectDetails)
	if message.Role != ToolResultMessageRole || message.ToolResult == nil {
		t.Fatalf("message variant = %+v", message)
	}
	if string(message.ToolResult.Details) != string(objectDetails.Details) {
		t.Fatalf("variant details lost")
	}

	context := Context{Messages: []Message{message}}
	if len(context.Messages) != 1 {
		t.Fatal("context lost messages")
	}
}

// Verifies absent/null/zero stay distinguishable across the message union.
func TestMessagePresenceDistinctions(t *testing.T) {
	emptyThinking := ThinkingBlock("")
	raw, err := json.Marshal(emptyThinking)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"type":"thinking","thinking":""}` {
		t.Fatalf("empty thinking marshaled as %s", raw)
	}

	redacted := RedactedThinkingBlock("cipher")
	raw, err = json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"redacted":true`) || !strings.Contains(string(raw), `"thinkingSignature":"cipher"`) {
		t.Fatalf("redacted thinking marshaled as %s", raw)
	}

	text := TextBlock("hello")
	raw, err = json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"type":"text","text":"hello"}` {
		t.Fatalf("text block marshaled as %s", raw)
	}

	signed := TextBlockSigned("hello", "")
	raw, err = json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"textSignature":""`) {
		t.Fatalf("present-empty signature dropped: %s", raw)
	}

	image := ImageBlock("ZGF0YQ==", "image/png")
	raw, err = json.Marshal(image)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"type":"image","data":"ZGF0YQ==","mimeType":"image/png"}` {
		t.Fatalf("image block marshaled as %s", raw)
	}

	call := ToolCallBlock(NewToolCall("c1", "echo", json.RawMessage(`{"n":1.5,"big":12345678901234567890}`)))
	raw, err = json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"arguments":{"n":1.5,"big":12345678901234567890}`) {
		t.Fatalf("tool call arguments rewritten: %s", raw)
	}

	var blocks []ContentBlock
	if err := json.Unmarshal([]byte(`[{"type":"text","text":"a"},{"type":"toolCall","id":"1","name":"n","arguments":{}},{"type":"image","data":"x","mimeType":"image/png"}]`), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || !blocks[0].IsText() || !blocks[1].IsToolCall() || !blocks[2].IsImage() {
		t.Fatalf("ordered content lost: %+v", blocks)
	}
}

// Verifies the string/array content forms stay distinct for system and user
// messages, and that a section can be removed with an explicit null.
func TestSystemAndUserContentForms(t *testing.T) {
	system := NewSystemMessage("base", 1)
	system.Sections = SystemSections{}
	system.Sections.SetSection("runtime", "context")
	system.Sections.RemoveSection("runtime-removed")
	raw, err := json.Marshal(system)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content":"base"`) {
		t.Fatalf("system content = %s", raw)
	}
	if !strings.Contains(string(raw), `"runtime-removed":null`) {
		t.Fatalf("removed section not null: %s", raw)
	}
	if !strings.Contains(string(raw), `"runtime":"context"`) {
		t.Fatalf("section value missing: %s", raw)
	}

	// A decoded system message restores the section map, including explicit
	// nulls that remove a section.
	var withSections SystemMessage
	if err := json.Unmarshal([]byte(`{"role":"system","content":"x","sections":{"kept":"v","dropped":null},"timestamp":1}`), &withSections); err != nil {
		t.Fatal(err)
	}
	if withSections.Sections["kept"] == nil || *withSections.Sections["kept"] != "v" {
		t.Fatalf("decoded sections = %+v", withSections.Sections)
	}
	if value, ok := withSections.Sections["dropped"]; !ok || value != nil {
		t.Fatalf("removed section not preserved: %+v", withSections.Sections)
	}

	structured := SystemMessage{Role: SystemMessageRole, Content: SystemContentBlocks([]TextContent{{Type: ContentTypeText, Text: "hi"}})}
	raw, err = json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content":[{"type":"text","text":"hi"}]`) {
		t.Fatalf("structured system content = %s", raw)
	}

	emptyBlocks := SystemMessage{Role: SystemMessageRole, Content: SystemContentBlocks(nil)}
	raw, err = json.Marshal(emptyBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content":[]`) {
		t.Fatalf("empty structured content = %s", raw)
	}

	var decoded SystemMessage
	if err := json.Unmarshal([]byte(`{"role":"system","content":"x","timestamp":1}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Content.Structured || decoded.Content.Text != "x" {
		t.Fatalf("decoded string form = %+v", decoded.Content)
	}

	user := NewUserMessage("", 2)
	raw, err = json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content":""`) {
		t.Fatalf("empty user content = %s", raw)
	}

	userBlocks := NewUserMessageBlocks([]ContentBlock{ImageBlock("x", "image/png")}, 2)
	raw, err = json.Marshal(userBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content":[{"type":"image"`) {
		t.Fatalf("user blocks = %s", raw)
	}
}

// Verifies the normalized transcript context folds the shorthand into a
// leading system message and does not mutate the raw context.
func TestNormalizeContextFoldsShorthand(t *testing.T) {
	prompt := "system prompt"
	raw := Context{
		SystemPrompt: &prompt,
		Messages:     []Message{NewUserMessageVariant(NewUserMessage("hi", 2))},
		Tools:        []Tool{NewTool("echo", "echo", json.RawMessage(`{"type":"object"}`))},
	}
	normalized := NormalizeContext(raw)
	if len(normalized.Messages) != 2 {
		t.Fatalf("normalized messages = %d; want 2", len(normalized.Messages))
	}
	leading := normalized.Messages[0]
	if leading.Role != SystemMessageRole || leading.System == nil {
		t.Fatalf("leading message = %+v", leading)
	}
	if leading.System.Content.Text != "system prompt" {
		t.Fatalf("leading content = %q", leading.System.Content.Text)
	}
	if len(leading.System.ToolsAdded) != 1 || leading.System.ToolsAdded[0].Name != "echo" {
		t.Fatalf("leading tools = %+v", leading.System.ToolsAdded)
	}

	noShorthand := NormalizeContext(Context{Messages: []Message{}})
	if len(noShorthand.Messages) != 0 {
		t.Fatalf("empty context normalized to %d messages", len(noShorthand.Messages))
	}
}

// Verifies the assistant message event constructors and the done/error reason
// narrowing used by the stream protocol.
func TestAssistantMessageEventConstructors(t *testing.T) {
	message := NewAssistantMessage(ApiOpenAICompletions, ProviderOpenAI, "fixture", 1)
	message.Content = []ContentBlock{TextBlock("hi")}
	message.StopReason = StopReasonStop

	done := NewDoneEvent(StopReasonStop, message)
	if done.Type != AssistantEventDone || done.Message == nil || !done.IsTerminal() {
		t.Fatalf("done event = %+v", done)
	}
	if done.Reason == nil || *done.Reason != StopReasonStop {
		t.Fatalf("done reason = %v", done.Reason)
	}

	start := NewStartEvent(message)
	if start.Type != AssistantEventStart || start.Partial == nil || start.IsTerminal() {
		t.Fatalf("start event = %+v", start)
	}

	delta := NewTextDeltaEvent(0, "x", message)
	if delta.ContentIndex == nil || *delta.ContentIndex != 0 || delta.Delta == nil || *delta.Delta != "x" {
		t.Fatalf("text delta = %+v", delta)
	}

	toolCall := NewToolCall("c1", "echo", json.RawMessage(`{}`))
	end := NewToolCallEndEvent(1, toolCall, message)
	if end.ToolCall == nil || end.ToolCall.Id != "c1" {
		t.Fatalf("toolcall end = %+v", end)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected done event to reject a non-done reason")
		}
	}()
	NewDoneEvent(StopReasonPending, message)
}

func TestAssistantMessageEventErrorReason(t *testing.T) {
	message := NewAssistantMessage(ApiOpenAICompletions, ProviderOpenAI, "fixture", 1)
	message.StopReason = StopReasonError
	err := NewErrorEvent(StopReasonError, message)
	if err.Type != AssistantEventError || err.Error == nil || !err.IsTerminal() {
		t.Fatalf("error event = %+v", err)
	}

	aborted := NewErrorEvent(StopReasonAborted, message)
	if aborted.Reason == nil || *aborted.Reason != StopReasonAborted {
		t.Fatalf("aborted reason = %v", aborted.Reason)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected error event to reject a non-error reason")
		}
	}()
	NewErrorEvent(StopReasonStop, message)
}

// Verifies the usage fields, including the absent-vs-zero reasoning breakdown.
func TestUsagePresence(t *testing.T) {
	usage := Usage{Input: 10, Output: 3, CacheRead: 2, CacheWrite: 1, TotalTokens: 16}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reasoning") {
		t.Fatalf("absent reasoning emitted: %s", raw)
	}

	reasoning := 0.0
	usage.Reasoning = &reasoning
	raw, err = json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reasoning":0`) {
		t.Fatalf("zero reasoning dropped: %s", raw)
	}

	cacheWrite1h := 0.5
	usage.CacheWrite1h = &cacheWrite1h
	var roundTripped Usage
	data, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(usage, roundTripped) {
		t.Fatalf("usage round trip = %+v; want %+v", roundTripped, usage)
	}
}

// Verifies the model optional fields and the per-API compat round trip.
func TestModelCompatRoundTrip(t *testing.T) {
	supportsStore := false
	model := Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           ApiOpenAICompletions,
		Provider:      ProviderOpenAI,
		BaseUrl:       "https://example.test",
		Reasoning:     true,
		Input:         []ModelInputModality{ModelInputText, ModelInputImage},
		Cost:          ModelCost{ModelCostRates: ModelCostRates{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 0.25}},
		ContextWindow: 8192,
		MaxTokens:     1024,
		Compat: Compat{OpenAICompletions: &OpenAICompletionsCompat{
			SupportsStore:  &supportsStore,
			ThinkingFormat: strPtr("qwen"),
		}},
	}
	data, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"supportsStore":false`) {
		t.Fatalf("present-false compat dropped: %s", data)
	}

	var decoded Model
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Compat.OpenAICompletions == nil || decoded.Compat.OpenAICompletions.SupportsStore == nil {
		t.Fatalf("compat not restored: %+v", decoded.Compat)
	}
	if *decoded.Compat.OpenAICompletions.SupportsStore {
		t.Fatal("compat value changed")
	}
	if decoded.Provider != ProviderOpenAI || decoded.Provider.Known() != true {
		t.Fatalf("provider lost: %q", decoded.Provider)
	}
	if !decoded.SupportsImageInput() {
		t.Fatal("image modality lost")
	}
}

// Verifies unknown compat keys survive a read/write cycle.
func TestModelPreservesUnknownCompatKeys(t *testing.T) {
	raw := `{"id":"x","name":"X","api":"openai-completions","provider":"openai","baseUrl":"u","reasoning":false,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":1,"maxTokens":1,"compat":{"supportsStore":true,"futureFlag":7}}`
	var model Model
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	// The concrete struct is selected for a known API, so the unknown key is
	// preserved verbatim only until the caller replaces Compat.
	var probe map[string]any
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatal(err)
	}
	compat, ok := probe["compat"].(map[string]any)
	if !ok {
		t.Fatalf("compat = %v", probe["compat"])
	}
	if compat["supportsStore"] != true {
		t.Fatalf("compat supportsStore = %v", compat["supportsStore"])
	}

	model.Compat = Compat{}
	encoded, err = json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatal(err)
	}
	compat, ok = probe["compat"].(map[string]any)
	if !ok || compat["futureFlag"].(float64) != 7 {
		t.Fatalf("verbatim compat payload lost: %v", probe["compat"])
	}
}

// Verifies thinking level maps distinguish absent, supported and unsupported.
func TestThinkingLevelMap(t *testing.T) {
	var levels ThinkingLevelMap
	if _, mapped, supported := levels.Lookup(ThinkingLow); mapped || supported {
		t.Fatal("nil map should report no mapping")
	}

	levels = ThinkingLevelMap{}
	levels.Set(ThinkingLow, "low-effort")
	levels.Unsupported(ThinkingHigh)

	value, mapped, supported := levels.Lookup(ThinkingLow)
	if !mapped || !supported || value != "low-effort" {
		t.Fatalf("low = %q, %v, %v", value, mapped, supported)
	}
	value, mapped, supported = levels.Lookup(ThinkingHigh)
	if !mapped || supported || value != "" {
		t.Fatalf("high = %q, %v, %v", value, mapped, supported)
	}
	if _, mapped, _ := levels.Lookup(ThinkingMax); mapped {
		t.Fatal("max should be absent")
	}

	raw, err := json.Marshal(levels)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"high":null`) {
		t.Fatalf("unsupported level not marshaled as null: %s", raw)
	}
}

// Verifies tool constrained sampling and input descriptor round trips.
func TestToolConstrainedSampling(t *testing.T) {
	tool := NewTool("echo", "echo", json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`))
	strict := ConstrainedStrictRequire
	sampling := NewJSONSchemaSampling(strict)
	tool.ConstrainedSampling = &sampling

	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"strict":"require"`) {
		t.Fatalf("constrained sampling = %s", raw)
	}
	if !strings.Contains(string(raw), `"n":{"type":"number"}`) {
		t.Fatalf("tool schema rewritten: %s", raw)
	}

	grammar := NewGrammarSampling(GrammarVariants{GrammarFormatOpenAILark: "start: \"a\""})
	raw, err = json.Marshal(grammar)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"openai_lark":"start: \"a\""`) {
		t.Fatalf("grammar variants = %s", raw)
	}

	object := tool.ArgsObject(json.RawMessage(`{"n":1,"s":"x"}`))
	if object == nil || object["s"] != "x" {
		t.Fatalf("args object = %v", object)
	}
	if tool.ArgsObject(json.RawMessage(`[1]`)) != nil {
		t.Fatal("array arguments should not decode as an object")
	}
}

// Verifies the deferred handle and chat template kwarg wire shapes.
func TestDeferredHandleAndChatTemplateKwargs(t *testing.T) {
	expires := int64(10)
	poll := int64(5)
	handle := DeferredHandle{
		Provider:    ProviderOpenAI,
		ModelId:     "fixture",
		Api:         ApiOpenAIResponses,
		Id:          "resp_1",
		ExpiresAt:   &expires,
		PollAfterMs: &poll,
		Data:        json.RawMessage(`{"row":2}`),
	}
	raw, err := json.Marshal(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expiresAt":10`) || !strings.Contains(string(raw), `"pollAfterMs":5`) {
		t.Fatalf("deferred handle = %s", raw)
	}

	omit := true
	kwargs := map[string]ChatTemplateKwargValue{
		"enable_thinking": ChatTemplateVarValue(ChatTemplateVarRef{Var: ChatTemplateVarThinkingEnabled, OmitWhenOff: &omit}),
		"top_k":           ChatTemplateNumber(5),
		"label":           ChatTemplateString("x"),
		"flag":            ChatTemplateBool(true),
		"nothing":         ChatTemplateNull(),
	}
	raw, err = json.Marshal(kwargs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"$var":"thinking.enabled"`) || !strings.Contains(string(raw), `"omitWhenOff":true`) {
		t.Fatalf("template kwarg var = %s", raw)
	}
	if !strings.Contains(string(raw), `"top_k":5`) || !strings.Contains(string(raw), `"label":"x"`) {
		t.Fatalf("template scalar kwargs = %s", raw)
	}
	if !strings.Contains(string(raw), `"nothing":null`) {
		t.Fatalf("template null kwarg = %s", raw)
	}

	var decoded map[string]ChatTemplateKwargValue
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	ref, ok := decoded["enable_thinking"].Value().(ChatTemplateVarRef)
	if !ok || ref.Var != ChatTemplateVarThinkingEnabled || ref.OmitWhenOff == nil || !*ref.OmitWhenOff {
		t.Fatalf("decoded var ref = %#v", decoded["enable_thinking"].Value())
	}
	if decoded["label"].Value() != "x" {
		t.Fatalf("decoded string kwarg = %#v", decoded["label"].Value())
	}
}

// Verifies the known identifier vocabularies keep their wire spellings.
func TestKnownIdentifiers(t *testing.T) {
	apis := KnownApis()
	if len(apis) != 10 || apis[0] != ApiOpenAICompletions || apis[9] != ApiPiMessages {
		t.Fatalf("known apis = %v", apis)
	}
	if Api("custom").Known() || !Api(ApiAnthropicMessages).Known() {
		t.Fatal("Api.Known mismatch")
	}
	providers := KnownProviders()
	if len(providers) != 41 {
		t.Fatalf("known providers = %d", len(providers))
	}
	if providers[0] != ProviderAmazonBedrock || providers[len(providers)-1] != ProviderXiaomiTokenPlanSGP {
		t.Fatalf("provider order = %v", providers)
	}
	if ProviderId("openai").Known() != true || ProviderId("custom").Known() {
		t.Fatal("ProviderId.Known mismatch")
	}
	if StopReason(ToolChoiceAuto).IsDoneReason() {
		t.Fatal("tool choice must not be treated as a done reason")
	}
	for _, reason := range DoneStopReasons() {
		if !reason.IsDoneReason() {
			t.Fatalf("%s should be a done reason", reason)
		}
	}
}

// Verifies the text signature parser accepts v1 payloads and rejects legacy ids.
func TestParseTextSignatureV1(t *testing.T) {
	signature := ParseTextSignatureV1(`{"v":1,"id":"msg_1","phase":"final_answer"}`)
	if signature == nil || signature.Id != "msg_1" || signature.Phase == nil || *signature.Phase != TextSignaturePhaseFinalAnswer {
		t.Fatalf("signature = %+v", signature)
	}
	if sig := ParseTextSignatureV1(`msg_1`); sig != nil {
		t.Fatalf("legacy id parsed as v1: %+v", sig)
	}
	if sig := ParseTextSignatureV1(`{"v":2,"id":"x"}`); sig != nil {
		t.Fatalf("v2 signature parsed: %+v", sig)
	}
	withoutPhase := ParseTextSignatureV1(`{"v":1,"id":"msg_2"}`)
	if withoutPhase == nil || withoutPhase.Phase != nil {
		t.Fatalf("phase should be absent: %+v", withoutPhase)
	}
}

// Verifies the images message types reuse the shared content union.
func TestAssistantImages(t *testing.T) {
	images := AssistantImages{
		Api:        ApiOpenRouterImages,
		Provider:   ProviderOpenRouterImages,
		Model:      "fixture",
		Output:     []ImagesOutputContent{ImageBlock("ZGF0YQ==", "image/png")},
		StopReason: ImagesStopReasonStop,
		Timestamp:  1,
	}
	raw, err := json.Marshal(images)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"output":[{"type":"image"`) {
		t.Fatalf("images output = %s", raw)
	}
	if strings.Contains(string(raw), `"responseId"`) || strings.Contains(string(raw), `"usage"`) {
		t.Fatalf("absent optional fields emitted: %s", raw)
	}

	context := ImagesContext{Input: []ImagesInputContent{TextBlock("draw")}}
	raw, err = json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"input":[{"type":"text"`) {
		t.Fatalf("images context = %s", raw)
	}
}

func strPtr(value string) *string { return &value }
