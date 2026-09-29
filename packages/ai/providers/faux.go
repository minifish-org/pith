// This file is a Go port of packages/ai/src/providers/faux.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A scripted in-memory provider used by tests: queued responses, token-sized
// text/thinking/tool-call deltas, prompt-cache usage estimation and deferred
// responses. It never performs network I/O.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/ai"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const (
	fauxDefaultProvider     = "faux"
	fauxDefaultModelID      = "faux-1"
	fauxDefaultModelName    = "Faux Model"
	fauxDefaultBaseURL      = "http://localhost:0"
	fauxDefaultMinTokenSize = 3
	fauxDefaultMaxTokenSize = 5
)

// FauxDefaultAPI is the api id used when a faux provider is created without an
// explicit one.
//
// Deliberate difference: upstream generates a random per-instance id
// (`faux:<Date.now()>:<random>`). A deterministic conformance suite cannot
// reproduce a random value, so the Go port pins the id recorded by the frozen
// upstream fixture. Functional streaming behavior is unchanged; callers that
// need to distinguish several providers pass RegisterFauxProviderOptions.API.
const FauxDefaultAPI = "faux:1790606749828:kijm22ry1xp"

// FauxModelDefinition describes one scripted faux model.
//
// Ports `FauxModelDefinition` from packages/ai/src/providers/faux.ts.
type FauxModelDefinition struct {
	ID            string                     `json:"id"`
	Name          string                     `json:"name,omitempty"`
	Reasoning     *bool                      `json:"reasoning,omitempty"`
	Input         []types.ModelInputModality `json:"input,omitempty"`
	InputLimits   *types.ModelInputLimits    `json:"inputLimits,omitempty"`
	Cost          *types.ModelCostRates      `json:"cost,omitempty"`
	ContextWindow *float64                   `json:"contextWindow,omitempty"`
	MaxTokens     *float64                   `json:"maxTokens,omitempty"`
}

// FauxContentBlock is a scripted assistant content block.
//
// Ports the `TextContent | ThinkingContent | ToolCall` union of
// packages/ai/src/providers/faux.ts.
type FauxContentBlock = types.ContentBlock

// FauxText builds a text content block.
//
// Ports `fauxText` from packages/ai/src/providers/faux.ts.
func FauxText(text string) types.ContentBlock { return types.TextBlock(text) }

// FauxThinking builds a thinking content block.
//
// Ports `fauxThinking` from packages/ai/src/providers/faux.ts.
func FauxThinking(thinking string) types.ContentBlock { return types.ThinkingBlock(thinking) }

// FauxToolCallOptions are the optional fields of FauxToolCall.
type FauxToolCallOptions struct {
	// ID overrides the generated tool call id.
	ID string
}

// FauxToolCall builds a tool call content block.
//
// Ports `fauxToolCall` from packages/ai/src/providers/faux.ts.
func FauxToolCall(name string, arguments json.RawMessage, options ...FauxToolCallOptions) types.ContentBlock {
	id := ""
	if len(options) > 0 {
		id = options[0].ID
	}
	if id == "" {
		id = randomID("tool")
	}
	return types.ToolCallBlock(types.ToolCall{
		Type:      types.ContentTypeToolCall,
		Id:        id,
		Name:      name,
		Arguments: append(json.RawMessage(nil), arguments...),
	})
}

// FauxAssistantMessageOptions are the optional fields of FauxAssistantMessage.
type FauxAssistantMessageOptions struct {
	StopReason   types.StopReason
	Deferred     *types.DeferredHandle
	ErrorMessage *string
	ResponseId   *string
	Timestamp    *float64
}

func normalizeFauxAssistantContent(content any) []types.ContentBlock {
	switch value := content.(type) {
	case nil:
		return []types.ContentBlock{}
	case string:
		return []types.ContentBlock{types.TextBlock(value)}
	case types.ContentBlock:
		return []types.ContentBlock{value}
	case []types.ContentBlock:
		return append([]types.ContentBlock(nil), value...)
	default:
		return []types.ContentBlock{}
	}
}

// FauxAssistantMessage builds a scripted assistant message with the default
// faux usage. The upstream signature accepts `string | block | block[]`.
//
// Ports `fauxAssistantMessage` from packages/ai/src/providers/faux.ts.
func FauxAssistantMessage(content any, options ...FauxAssistantMessageOptions) types.AssistantMessage {
	var optionsValue FauxAssistantMessageOptions
	if len(options) > 0 {
		optionsValue = options[0]
	}
	stopReason := optionsValue.StopReason
	if stopReason == "" {
		stopReason = types.StopReasonStop
	}
	timestamp := float64(time.Now().UnixMilli())
	if optionsValue.Timestamp != nil {
		timestamp = *optionsValue.Timestamp
	}
	return types.AssistantMessage{
		Role:         types.AssistantMessageRole,
		Content:      normalizeFauxAssistantContent(content),
		Api:          types.Api(FauxDefaultAPI),
		Provider:     types.ProviderId(fauxDefaultProvider),
		Model:        fauxDefaultModelID,
		Usage:        fauxDefaultUsage(),
		StopReason:   stopReason,
		Deferred:     optionsValue.Deferred,
		ErrorMessage: optionsValue.ErrorMessage,
		ResponseId:   optionsValue.ResponseId,
		Timestamp:    timestamp,
	}
}

func fauxDefaultUsage() types.Usage {
	return types.Usage{
		Input:       0,
		Output:      0,
		CacheRead:   0,
		CacheWrite:  0,
		TotalTokens: 0,
		Cost:        types.UsageCost{},
	}
}

// FauxProviderState is the mutable scripted-provider state.
//
// Ports `FauxProviderState` from packages/ai/src/providers/faux.ts.
type FauxProviderState struct {
	CallCount          int                    `json:"callCount"`
	DeferredFetchCount int                    `json:"deferredFetchCount"`
	CancelledDeferred  []types.DeferredHandle `json:"cancelledDeferred"`
}

// FauxResponseFactory computes a response from the transcript.
//
// Ports `FauxResponseFactory` from packages/ai/src/providers/faux.ts.
type FauxResponseFactory func(context types.TranscriptContext, options *types.SimpleStreamOptions, state *FauxProviderState, model types.Model) (types.AssistantMessage, error)

// FauxResponseStep is a queued response: either a literal message or a factory.
//
// Ports `FauxResponseStep` from packages/ai/src/providers/faux.ts.
type FauxResponseStep struct {
	Message *types.AssistantMessage
	Factory FauxResponseFactory
}

// FauxTokenSize bounds the random token chunk size.
type FauxTokenSize struct {
	Min int
	Max int
}

// FauxDeferredOptions configure scripted deferred responses.
type FauxDeferredOptions struct {
	PendingFetches int
	PollAfterMs    *int64
}

// RegisterFauxProviderOptions configure a faux provider.
//
// Ports `RegisterFauxProviderOptions` from packages/ai/src/providers/faux.ts.
type RegisterFauxProviderOptions struct {
	API             string
	Provider        string
	Models          []FauxModelDefinition
	Deferred        *FauxDeferredOptions
	TokensPerSecond *float64
	TokenSize       *FauxTokenSize
}

// FauxProviderRegistration is the registration handle returned by the compat
// registerFauxProvider entry point.
//
// Ports `FauxProviderRegistration` from packages/ai/src/providers/faux.ts.
type FauxProviderRegistration struct {
	API                     string
	Models                  []types.Model
	State                   *FauxProviderState
	GetModel                func(modelID ...string) *types.Model
	SetResponses            func(responses []FauxResponseStep)
	AppendResponses         func(responses []FauxResponseStep)
	GetPendingResponseCount func() int
	Unregister              func()
}

// FauxProviderHandle is the handle returned by FauxProvider.
//
// Ports `FauxProviderHandle` from packages/ai/src/providers/faux.ts.
type FauxProviderHandle struct {
	Provider                ai.Provider
	API                     string
	Models                  []types.Model
	State                   *FauxProviderState
	GetModel                func(modelID ...string) *types.Model
	SetResponses            func(responses []FauxResponseStep)
	AppendResponses         func(responses []FauxResponseStep)
	GetPendingResponseCount func() int
}

func randomID(prefix string) string {
	return fmt.Sprintf("%s:%d:%d", prefix, time.Now().UnixMilli(), rand.Int63())
}

func estimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// FauxCore is the scripted provider core.
//
// Ports `createFauxCore` from packages/ai/src/providers/faux.ts.
type FauxCore struct {
	API             string
	Provider        string
	Models          []types.Model
	State           *FauxProviderState
	TokensPerSecond *float64
	MinTokenSize    int
	MaxTokenSize    int
	Deferred        *FauxDeferredOptions

	mu               sync.Mutex
	pendingResponses []FauxResponseStep
	promptCache      map[string]string
	deferred         map[string]*fauxDeferredEntry
}

type fauxDeferredEntry struct {
	handle         types.DeferredHandle
	step           FauxResponseStep
	context        types.TranscriptContext
	options        *types.SimpleStreamOptions
	model          types.Model
	pendingFetches int
	cancelled      bool
	final          *types.AssistantMessage
}

// CreateFauxCore builds a scripted faux provider core.
//
// Ports `createFauxCore` from packages/ai/src/providers/faux.ts.
func CreateFauxCore(options RegisterFauxProviderOptions) *FauxCore {
	api := options.API
	if api == "" {
		api = FauxDefaultAPI
	}
	provider := options.Provider
	if provider == "" {
		provider = fauxDefaultProvider
	}
	minTokenSize := fauxDefaultMinTokenSize
	if options.TokenSize != nil {
		minTokenSize = options.TokenSize.Min
	}
	maxTokenSize := fauxDefaultMaxTokenSize
	if options.TokenSize != nil {
		maxTokenSize = options.TokenSize.Max
	}
	if minTokenSize < 1 {
		minTokenSize = 1
	}
	if minTokenSize > maxTokenSize {
		minTokenSize = maxTokenSize
	}
	if maxTokenSize < minTokenSize {
		maxTokenSize = minTokenSize
	}

	definitions := options.Models
	if len(definitions) == 0 {
		reasoning := false
		cost := types.ModelCostRates{}
		contextWindow := 128000.0
		maxTokens := 16384.0
		definitions = []FauxModelDefinition{{
			ID:            fauxDefaultModelID,
			Name:          fauxDefaultModelName,
			Reasoning:     &reasoning,
			Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
			Cost:          &cost,
			ContextWindow: &contextWindow,
			MaxTokens:     &maxTokens,
		}}
	}

	models := make([]types.Model, 0, len(definitions))
	for _, definition := range definitions {
		model := types.Model{
			Id:       definition.ID,
			Api:      types.Api(api),
			Provider: types.ProviderId(provider),
			BaseUrl:  fauxDefaultBaseURL,
			Input:    definition.Input,
		}
		model.Name = definition.Name
		if model.Name == "" {
			model.Name = definition.ID
		}
		if definition.Reasoning != nil {
			model.Reasoning = *definition.Reasoning
		}
		if model.Input == nil {
			model.Input = []types.ModelInputModality{types.ModelInputText, types.ModelInputImage}
		}
		model.InputLimits = definition.InputLimits
		if definition.Cost != nil {
			model.Cost = types.ModelCost{ModelCostRates: *definition.Cost}
		}
		if definition.ContextWindow != nil {
			model.ContextWindow = *definition.ContextWindow
		} else {
			model.ContextWindow = 128000
		}
		if definition.MaxTokens != nil {
			model.MaxTokens = *definition.MaxTokens
		} else {
			model.MaxTokens = 16384
		}
		models = append(models, model)
	}

	return &FauxCore{
		API:             api,
		Provider:        provider,
		Models:          models,
		State:           &FauxProviderState{},
		TokensPerSecond: options.TokensPerSecond,
		MinTokenSize:    minTokenSize,
		MaxTokenSize:    maxTokenSize,
		Deferred:        options.Deferred,
		promptCache:     map[string]string{},
		deferred:        map[string]*fauxDeferredEntry{},
	}
}

// GetModel returns the model with the given id, or the first model when the id
// is omitted.
func (c *FauxCore) GetModel(modelID ...string) *types.Model {
	if len(modelID) == 0 || modelID[0] == "" {
		if len(c.Models) == 0 {
			return nil
		}
		model := c.Models[0]
		return &model
	}
	for _, model := range c.Models {
		if model.Id == modelID[0] {
			copy := model
			return &copy
		}
	}
	return nil
}

// SetResponses replaces the pending response queue.
func (c *FauxCore) SetResponses(responses []FauxResponseStep) {
	c.mu.Lock()
	c.pendingResponses = append([]FauxResponseStep(nil), responses...)
	c.mu.Unlock()
}

// AppendResponses appends to the pending response queue.
func (c *FauxCore) AppendResponses(responses []FauxResponseStep) {
	c.mu.Lock()
	c.pendingResponses = append(c.pendingResponses, responses...)
	c.mu.Unlock()
}

// GetPendingResponseCount returns the number of queued responses.
func (c *FauxCore) GetPendingResponseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pendingResponses)
}

// DeferredCapabilities reports that the faux core supports deferred responses.
func (c *FauxCore) DeferredCapabilities() (bool, bool) { return true, true }

func splitStringByTokenSize(text string, minTokenSize, maxTokenSize int) []string {
	chunks := []string{}
	index := 0
	for index < len(text) {
		span := maxTokenSize - minTokenSize
		tokenSize := minTokenSize
		if span > 0 {
			tokenSize = minTokenSize + rand.Intn(span+1)
		}
		charSize := tokenSize * 4
		if charSize < 1 {
			charSize = 1
		}
		end := index + charSize
		if end > len(text) {
			end = len(text)
		}
		chunks = append(chunks, text[index:end])
		index = end
	}
	if len(chunks) == 0 {
		chunks = append(chunks, "")
	}
	return chunks
}

func contentToText(content types.UserContent) string {
	if content.Blocks == nil && !content.Structured {
		return content.Text
	}
	parts := make([]string, 0, len(content.Blocks))
	for _, block := range content.Blocks {
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		} else if block.Image != nil {
			parts = append(parts, fmt.Sprintf("[image:%s:%d]", block.Image.MimeType, len(block.Image.Data)))
		}
	}
	return strings.Join(parts, "\n")
}

func assistantContentToText(content []types.ContentBlock) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		switch {
		case block.Text != nil:
			parts = append(parts, block.Text.Text)
		case block.Thinking != nil:
			parts = append(parts, block.Thinking.Thinking)
		case block.ToolCall != nil:
			parts = append(parts, block.ToolCall.Name+":"+string(block.ToolCall.Arguments))
		}
	}
	return strings.Join(parts, "\n")
}

func toolResultToText(message types.ToolResultMessage) string {
	parts := []string{message.ToolName}
	for _, block := range message.Content {
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		} else if block.Image != nil {
			parts = append(parts, fmt.Sprintf("[image:%s:%d]", block.Image.MimeType, len(block.Image.Data)))
		}
	}
	return strings.Join(parts, "\n")
}

func messageToText(message types.Message) string {
	switch message.Role {
	case types.SystemMessageRole:
		if message.System == nil {
			return ""
		}
		parts := []string{utils.GetSystemMessageText(*message.System)}
		for _, tool := range message.System.ToolsRemoved {
			raw, _ := json.Marshal(tool)
			parts = append(parts, "tool-:"+string(raw))
		}
		for _, tool := range message.System.ToolsAdded {
			raw, _ := json.Marshal(tool)
			parts = append(parts, "tool+:"+string(raw))
		}
		filtered := parts[:0]
		for _, part := range parts {
			if part != "" {
				filtered = append(filtered, part)
			}
		}
		return strings.Join(filtered, "\n")
	case types.UserMessageRole:
		if message.User == nil {
			return ""
		}
		return contentToText(message.User.Content)
	case types.AssistantMessageRole:
		if message.Assistant == nil {
			return ""
		}
		return assistantContentToText(message.Assistant.Content)
	case types.ToolResultMessageRole:
		if message.ToolResult == nil {
			return ""
		}
		return toolResultToText(*message.ToolResult)
	default:
		return ""
	}
}

func serializeContext(context types.TranscriptContext) string {
	parts := make([]string, 0, len(context.Messages))
	for _, message := range context.Messages {
		parts = append(parts, message.Role+":"+messageToText(message))
	}
	return strings.Join(parts, "\n\n")
}

func commonPrefixLength(a, b string) int {
	length := len(a)
	if len(b) < length {
		length = len(b)
	}
	index := 0
	for index < length && a[index] == b[index] {
		index++
	}
	return index
}

func (c *FauxCore) withUsageEstimate(message types.AssistantMessage, context types.TranscriptContext, options *types.StreamOptions) types.AssistantMessage {
	promptText := serializeContext(context)
	promptTokens := estimateTokens(promptText)
	outputTokens := estimateTokens(assistantContentToText(message.Content))
	input := promptTokens
	cacheRead := 0
	cacheWrite := 0
	var sessionID *string
	if options != nil {
		sessionID = options.SessionId
	}
	if sessionID != nil && *sessionID != "" && (options.CacheRetention == nil || *options.CacheRetention != types.CacheRetentionNone) {
		c.mu.Lock()
		previousPrompt, ok := c.promptCache[*sessionID]
		if ok {
			cachedChars := commonPrefixLength(previousPrompt, promptText)
			cacheRead = estimateTokens(previousPrompt[:cachedChars])
			cacheWrite = estimateTokens(promptText[cachedChars:])
			input = promptTokens - cacheRead
			if input < 0 {
				input = 0
			}
		} else {
			cacheWrite = promptTokens
		}
		c.promptCache[*sessionID] = promptText
		c.mu.Unlock()
	}
	message.Usage = types.Usage{
		Input:       float64(input),
		Output:      float64(outputTokens),
		CacheRead:   float64(cacheRead),
		CacheWrite:  float64(cacheWrite),
		TotalTokens: float64(input + outputTokens + cacheRead + cacheWrite),
		Cost:        types.UsageCost{},
	}
	return message
}

func cloneMessage(message types.AssistantMessage, api string, provider string, modelID string) types.AssistantMessage {
	clone := message
	clone.Api = types.Api(api)
	clone.Provider = types.ProviderId(provider)
	clone.Model = modelID
	if clone.Timestamp == 0 {
		clone.Timestamp = float64(time.Now().UnixMilli())
	}
	return clone
}

func createErrorMessage(err error, api, provider, modelID string) types.AssistantMessage {
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	return types.AssistantMessage{
		Role:         types.AssistantMessageRole,
		Content:      []types.ContentBlock{},
		Api:          types.Api(api),
		Provider:     types.ProviderId(provider),
		Model:        modelID,
		Usage:        fauxDefaultUsage(),
		StopReason:   types.StopReasonError,
		ErrorMessage: &message,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
}

func createAbortedMessage(partial types.AssistantMessage) types.AssistantMessage {
	message := partial
	message.StopReason = types.StopReasonAborted
	text := "Request was aborted"
	message.ErrorMessage = &text
	message.Timestamp = float64(time.Now().UnixMilli())
	return message
}

func (c *FauxCore) scheduleChunk(ctx context.Context, chunk string) {
	if c.TokensPerSecond == nil || *c.TokensPerSecond <= 0 {
		return
	}
	delay := time.Duration(float64(estimateTokens(chunk)) / *c.TokensPerSecond * float64(time.Second))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (c *FauxCore) streamWithDeltas(stream *types.AssistantMessageEventStream, message types.AssistantMessage, signal <-chan struct{}) {
	partial := message
	partial.Content = []types.ContentBlock{}
	partial.StopReason = types.StopReasonPending
	if signal != nil {
		select {
		case <-signal:
			aborted := createAbortedMessage(partial)
			stream.Push(types.NewErrorEvent(types.StopReasonAborted, aborted))
			stream.End(&aborted)
			return
		default:
		}
	}
	stream.Push(types.NewStartEvent(partial))

	for index := 0; index < len(message.Content); index++ {
		if signal != nil {
			select {
			case <-signal:
				aborted := createAbortedMessage(partial)
				stream.Push(types.NewErrorEvent(types.StopReasonAborted, aborted))
				stream.End(&aborted)
				return
			default:
			}
		}
		block := message.Content[index]
		if block.Thinking != nil {
			partial.Content = append(partial.Content, types.ThinkingBlock(""))
			stream.Push(types.NewThinkingStartEvent(index, partial))
			for _, chunk := range splitStringByTokenSize(block.Thinking.Thinking, c.MinTokenSize, c.MaxTokenSize) {
				c.scheduleChunk(context.Background(), chunk)
				if partial.Content[index].Thinking != nil {
					partial.Content[index].Thinking.Thinking += chunk
				}
				stream.Push(types.NewThinkingDeltaEvent(index, chunk, partial))
			}
			stream.Push(types.NewThinkingEndEvent(index, block.Thinking.Thinking, partial))
			continue
		}
		if block.Text != nil {
			partial.Content = append(partial.Content, types.TextBlock(""))
			stream.Push(types.NewTextStartEvent(index, partial))
			for _, chunk := range splitStringByTokenSize(block.Text.Text, c.MinTokenSize, c.MaxTokenSize) {
				c.scheduleChunk(context.Background(), chunk)
				if partial.Content[index].Text != nil {
					partial.Content[index].Text.Text += chunk
				}
				stream.Push(types.NewTextDeltaEvent(index, chunk, partial))
			}
			stream.Push(types.NewTextEndEvent(index, block.Text.Text, partial))
			continue
		}
		if block.ToolCall != nil {
			partial.Content = append(partial.Content, types.ToolCallBlock(types.ToolCall{
				Type:      types.ContentTypeToolCall,
				Id:        block.ToolCall.Id,
				Name:      block.ToolCall.Name,
				Arguments: json.RawMessage("{}"),
			}))
			stream.Push(types.NewToolCallStartEvent(index, partial))
			for _, chunk := range splitStringByTokenSize(string(block.ToolCall.Arguments), c.MinTokenSize, c.MaxTokenSize) {
				c.scheduleChunk(context.Background(), chunk)
				stream.Push(types.NewToolCallDeltaEvent(index, chunk, partial))
			}
			if partial.Content[index].ToolCall != nil {
				partial.Content[index].ToolCall.Arguments = block.ToolCall.Arguments
			}
			stream.Push(types.NewToolCallEndEvent(index, *block.ToolCall, partial))
			continue
		}
	}

	if message.StopReason == types.StopReasonPending {
		err := fmt.Errorf("Faux response ended without a stop reason")
		failed := createErrorMessage(err, c.API, c.Provider, message.Model)
		stream.Push(types.NewErrorEvent(types.StopReasonError, failed))
		stream.End(&failed)
		return
	}
	if message.StopReason == types.StopReasonError || message.StopReason == types.StopReasonAborted {
		stream.Push(types.NewErrorEvent(message.StopReason, message))
		stream.End(&message)
		return
	}

	stream.Push(types.NewDoneEvent(message.StopReason, message))
	stream.End(&message)
}

func (c *FauxCore) resolveResponse(step FauxResponseStep, context types.TranscriptContext, options *types.SimpleStreamOptions, model types.Model) (types.AssistantMessage, error) {
	var resolved types.AssistantMessage
	if step.Factory != nil {
		message, err := step.Factory(context, options, c.State, model)
		if err != nil {
			return types.AssistantMessage{}, err
		}
		resolved = message
	} else if step.Message != nil {
		resolved = *step.Message
	}
	var streamOptions *types.StreamOptions
	if options != nil {
		streamOptions = &options.StreamOptions
	}
	return c.withUsageEstimate(cloneMessage(resolved, c.API, c.Provider, model.Id), context, streamOptions), nil
}

// Stream is the scripted streaming implementation.
func (c *FauxCore) Stream(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	var simple *types.SimpleStreamOptions
	if options != nil {
		simple = &types.SimpleStreamOptions{StreamOptions: *options}
	}
	return c.stream(model, context, simple)
}

// StreamSimple is the scripted simple-stream implementation.
func (c *FauxCore) StreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return c.stream(model, context, options)
}

func (c *FauxCore) stream(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	outer := types.NewAssistantMessageEventStream()
	c.mu.Lock()
	var step *FauxResponseStep
	if len(c.pendingResponses) > 0 {
		value := c.pendingResponses[0]
		c.pendingResponses = c.pendingResponses[1:]
		step = &value
	}
	c.mu.Unlock()
	c.State.CallCount++

	transcript := types.TranscriptContext{}
	if context != nil {
		transcript = *context
	}
	modelValue := types.Model{}
	if model != nil {
		modelValue = *model
	}

	go func() {
		if step == nil {
			message := createErrorMessage(fmt.Errorf("No more faux responses queued"), c.API, c.Provider, modelValue.Id)
			var streamOptions *types.StreamOptions
			if options != nil {
				streamOptions = &options.StreamOptions
			}
			message = c.withUsageEstimate(message, transcript, streamOptions)
			outer.Push(types.NewErrorEvent(types.StopReasonError, message))
			outer.End(&message)
			return
		}

		var signal <-chan struct{}
		if options != nil {
			signal = options.Signal
		}

		if options != nil && options.Deferred != nil {
			handle := types.DeferredHandle{
				Provider: modelValue.Provider,
				ModelId:  modelValue.Id,
				Api:      modelValue.Api,
				Id:       randomID("deferred"),
			}
			if c.Deferred != nil && c.Deferred.PollAfterMs != nil {
				handle.PollAfterMs = c.Deferred.PollAfterMs
			}
			pendingFetches := 0
			if c.Deferred != nil && c.Deferred.PendingFetches > 0 {
				pendingFetches = c.Deferred.PendingFetches
			}
			c.mu.Lock()
			c.deferred[handle.Id] = &fauxDeferredEntry{
				handle:         handle,
				step:           *step,
				context:        transcript,
				options:        options,
				model:          modelValue,
				pendingFetches: pendingFetches,
			}
			c.mu.Unlock()
			message := types.AssistantMessage{
				Role:       types.AssistantMessageRole,
				Content:    []types.ContentBlock{},
				Api:        modelValue.Api,
				Provider:   modelValue.Provider,
				Model:      modelValue.Id,
				Usage:      fauxDefaultUsage(),
				StopReason: types.StopReasonDeferred,
				Deferred:   &handle,
				Timestamp:  float64(time.Now().UnixMilli()),
			}
			c.streamWithDeltas(outer, message, signal)
			return
		}

		message, err := c.resolveResponse(*step, transcript, options, modelValue)
		if err != nil {
			failed := createErrorMessage(err, c.API, c.Provider, modelValue.Id)
			outer.Push(types.NewErrorEvent(types.StopReasonError, failed))
			outer.End(&failed)
			return
		}
		c.streamWithDeltas(outer, message, signal)
	}()

	return outer
}

// FetchDeferred resumes a scripted deferred response.
func (c *FauxCore) FetchDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	outer := types.NewAssistantMessageEventStream()
	c.State.DeferredFetchCount++
	modelValue := types.Model{}
	if model != nil {
		modelValue = *model
	}
	go func() {
		c.mu.Lock()
		entry, ok := c.deferred[handle.Id]
		c.mu.Unlock()
		if !ok || entry.handle.Provider != handle.Provider || entry.handle.ModelId != handle.ModelId || entry.handle.Api != handle.Api {
			err := fmt.Errorf("Unknown faux deferred response: %s", handle.Id)
			failed := createErrorMessage(err, c.API, c.Provider, modelValue.Id)
			outer.Push(types.NewErrorEvent(types.StopReasonError, failed))
			outer.End(&failed)
			return
		}
		if entry.cancelled {
			err := fmt.Errorf("Faux deferred response was cancelled: %s", handle.Id)
			failed := createErrorMessage(err, c.API, c.Provider, modelValue.Id)
			outer.Push(types.NewErrorEvent(types.StopReasonError, failed))
			outer.End(&failed)
			return
		}
		if entry.pendingFetches > 0 {
			entry.pendingFetches--
			c.streamWithDeltas(outer, deferredMessage(modelValue, entry.handle), nil)
			return
		}
		if entry.final == nil {
			final, err := c.resolveResponse(entry.step, entry.context, entry.options, entry.model)
			if err != nil {
				failed := createErrorMessage(err, c.API, c.Provider, entry.model.Id)
				entry.final = &failed
			} else {
				entry.final = &final
			}
		}
		c.streamWithDeltas(outer, *entry.final, nil)
	}()
	return outer, nil
}

func deferredMessage(model types.Model, handle types.DeferredHandle) types.AssistantMessage {
	return types.AssistantMessage{
		Role:       types.AssistantMessageRole,
		Content:    []types.ContentBlock{},
		Api:        model.Api,
		Provider:   model.Provider,
		Model:      model.Id,
		Usage:      fauxDefaultUsage(),
		StopReason: types.StopReasonDeferred,
		Deferred:   &handle,
		Timestamp:  float64(time.Now().UnixMilli()),
	}
}

// CancelDeferred marks a scripted deferred response cancelled.
func (c *FauxCore) CancelDeferred(model *types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error {
	c.mu.Lock()
	clone := handle
	c.State.CancelledDeferred = append(c.State.CancelledDeferred, clone)
	if entry, ok := c.deferred[handle.Id]; ok {
		entry.cancelled = true
	}
	c.mu.Unlock()
	return nil
}

// FauxProvider builds a faux provider handle. The scripted core is accessible
// through the returned handle.
//
// Ports `fauxProvider` from packages/ai/src/providers/faux.ts.
func FauxProvider(options ...RegisterFauxProviderOptions) FauxProviderHandle {
	var value RegisterFauxProviderOptions
	if len(options) > 0 {
		value = options[0]
	}
	core := CreateFauxCore(value)
	fauxAuth := authtypes.ApiKeyAuth{
		Name: "Faux",
		Resolve: func(context.Context, authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
			return &authtypes.AuthResult{}, nil
		},
	}
	provider := ai.CreateProvider(ai.CreateProviderOptions{
		ID:     core.Provider,
		Auth:   authtypes.ProviderAuth{APIKey: &fauxAuth},
		Models: core.Models,
		API:    core,
	})
	return FauxProviderHandle{
		Provider:                provider,
		API:                     core.API,
		Models:                  core.Models,
		State:                   core.State,
		GetModel:                core.GetModel,
		SetResponses:            core.SetResponses,
		AppendResponses:         core.AppendResponses,
		GetPendingResponseCount: core.GetPendingResponseCount,
	}
}
