// This file is a Go port of packages/ai/src/api/bedrock-converse-stream.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream module talks to Amazon Bedrock through the AWS SDK for
// JavaScript v3. The Go port uses the AWS SDK for Go v2
// (github.com/aws/aws-sdk-go-v2/service/bedrockruntime) for request/response
// modeling, SigV4/bearer authentication, endpoint resolution and the binary
// application/vnd.amazon.eventstream framing. It speaks ConverseStream and
// preserves the observable contract: credential/profile resolution, region and
// endpoint selection, cache points, reasoning/redaction, custom headers,
// response-header callbacks, raw stop reasons, usage/cost accounting and the
// full assistant event sequence.
//
// The injectable `client` becomes the BedrockClient interface so callers and
// tests can substitute a transport without depending on a live AWS account.
package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithydocument "github.com/aws/smithy-go/document"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// BedrockThinkingDisplay controls how Claude thinking content is returned in
// responses.
type BedrockThinkingDisplay string

// Thinking display modes.
const (
	BedrockThinkingDisplaySummarized BedrockThinkingDisplay = "summarized"
	BedrockThinkingDisplayOmitted    BedrockThinkingDisplay = "omitted"
)

// BedrockToolChoice is the forced-tool form of the Bedrock tool choice
// (upstream `{ type: "tool"; name: string }`).
type BedrockToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// BedrockClient is the injectable Bedrock transport. When set on BedrockOptions
// it replaces internal client construction entirely, mirroring the upstream
// `client` test seam. The concrete *bedrockruntime.Client satisfies it.
type BedrockClient interface {
	ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
}

// BedrockOptions are the Bedrock ConverseStream-specific stream options.
type BedrockOptions struct {
	types.StreamOptions

	// Region pins the AWS region. When empty the standard env chain is used.
	Region *string
	// Profile selects a named AWS shared-config profile. It wins over ambient
	// env access keys, matching the upstream auth-flow ordering.
	Profile *string
	// ToolChoice is "auto", "any", "none", or BedrockToolChoice{Type:"tool"}.
	ToolChoice any
	// Reasoning enables extended thinking for supported models.
	Reasoning *types.ThinkingLevel
	// ThinkingBudgets overrides the default token budget per thinking level.
	ThinkingBudgets *types.ThinkingBudgets
	// InterleavedThinking requests the interleaved-thinking beta for
	// token-budget thinking models (default true).
	InterleavedThinking *bool
	// ThinkingDisplay selects summarized or omitted thinking content.
	ThinkingDisplay *BedrockThinkingDisplay
	// RequestMetadata attaches cost-allocation tags to the inference request.
	RequestMetadata map[string]string
	// BearerToken authenticates with a Bedrock API key instead of SigV4.
	BearerToken *string
	// Client is a pre-built transport. When set, internal client construction is
	// skipped.
	Client BedrockClient
}

const (
	bedrockEmptyTextPlaceholder         = "<empty>"
	bedrockRedactedThinkingPlaceholder  = "[Reasoning redacted]"
	bedrockDataRetentionDocsURL         = "https://docs.aws.amazon.com/bedrock/latest/userguide/data-retention.html"
	maxBedrockDiagnosticValueChars      = 200
	bedrockInterleavedThinkingBetaValue = "interleaved-thinking-2025-05-14"
)

// bedrockErrorPrefixes maps Bedrock SDK exception names to stable,
// human-readable prefixes. Downstream retry logic matches patterns such as
// `server.?error` and `service.?unavailable`, so the legacy prefix format is
// preserved rather than the raw SDK exception name.
var bedrockErrorPrefixes = map[string]string{
	"InternalServerException":     "Internal server error",
	"ModelStreamErrorException":   "Model stream error",
	"ValidationException":         "Validation error",
	"ThrottlingException":         "Throttling error",
	"ServiceUnavailableException": "Service unavailable",
}

// bedrockReservedHeaderExact are headers that must never be overwritten by
// caller-supplied headers. `host` and the `x-amz-*` headers participate in the
// SigV4 canonical request; `authorization` is owned by SigV4 or the bearer
// token path.
var bedrockReservedHeaderExact = map[string]bool{
	"authorization": true,
	"host":          true,
}

func isReservedBedrockHeader(key string) bool {
	lower := strings.ToLower(key)
	return strings.HasPrefix(lower, "x-amz-") || bedrockReservedHeaderExact[lower]
}

var (
	bedrockARNRegionPattern   = regexp.MustCompile(`^arn:aws(?:-[a-z0-9-]+)?:bedrock:([a-z0-9-]+):`)
	bedrockEndpointPattern    = regexp.MustCompile(`^bedrock-runtime(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)
	bedrockWhitespaceSep      = regexp.MustCompile(`[\s_.:]+`)
	bedrockDataRetentionMatch = regexp.MustCompile(`data retention mode`)
)

// =============================================================================
// Stream entry points
// =============================================================================

// BedrockConverseStream is the streaming entry point for the Bedrock
// ConverseStream API. It returns immediately; request setup and consumption run
// on a background goroutine and are delivered through the stream protocol.
func BedrockConverseStream(model *types.Model, transcript *types.TranscriptContext, options *BedrockOptions) *types.AssistantMessageEventStream {
	if options == nil {
		options = &BedrockOptions{}
	}
	stream := types.NewAssistantMessageEventStream()
	// Bedrock has no mid-conversation system messages; fold them into the
	// leading prompt.
	normalizedContext := utils.CollapseSystemMessages(*transcript)

	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		state := &bedrockStreamState{
			output:         &output,
			positions:      map[int]int{},
			partialJSON:    map[int]string{},
			redactedChunks: map[int][][]byte{},
		}

		signal := options.Signal
		fail := func(err error) {
			state.finalizeAll()
			output.StopReason = types.StopReasonError
			if aborted(signal) {
				output.StopReason = types.StopReasonAborted
			}
			message := "An unknown error occurred"
			if err != nil {
				message = formatBedrockError(err)
			}
			output.ErrorMessage = &message
			if output.StopReason == types.StopReasonError {
				appendBedrockFailureDiagnostic(&output, err, state.responseRequestId)
			}
			stream.Push(types.NewErrorEvent(output.StopReason, output))
			stream.End(&output)
		}

		ctx, cancel := contextForSignal(contextBackground(), signal)
		defer cancel()

		var client BedrockClient
		observedRawResponse := false
		if options.Client != nil {
			client = options.Client
		} else {
			awsCfg, err := resolveBedrockAWSConfig(ctx, model, options, &observedRawResponse)
			if err != nil {
				fail(err)
				return
			}
			// Install the reader-only request-body boundary after NewFromConfig
			// has resolved its HTTP client defaults. Wrapping cfg.HTTPClient
			// before this point would hide the SDK's BuildableClient type and
			// skip its dialer, TLS, and read-timeout initialization. The
			// callback below runs after resolveHTTPClient, so the resolved
			// client (including a proxy-configured *http.Client) is preserved
			// and only wrapped.
			client = bedrockruntime.NewFromConfig(awsCfg, func(o *bedrockruntime.Options) {
				o.HTTPClient = newBedrockHTTPClient(o.HTTPClient)
			})
		}

		supportsStrictMode := false
		if model.Compat.Bedrock != nil && model.Compat.Bedrock.SupportsStrictMode != nil {
			supportsStrictMode = *model.Compat.Bedrock.SupportsStrictMode
		}
		cacheRetention := resolveCacheRetention(options.CacheRetention, options.Env)

		inferenceMaxTokens := options.MaxTokens
		if inferenceMaxTokens == nil && isAnthropicClaudeModel(model) {
			value := int(model.MaxTokens)
			inferenceMaxTokens = &value
		}

		initialSystemMessage := utils.GetInitialSystemMessage(normalizedContext.Messages)
		var initialSystemPrompt *string
		if initialSystemMessage != nil {
			text := utils.GetSystemMessageText(*initialSystemMessage)
			initialSystemPrompt = &text
		}

		messages, err := convertBedrockMessages(normalizedContext, model, cacheRetention, options.Env)
		if err != nil {
			fail(err)
			return
		}
		system, err := buildBedrockSystemPrompt(initialSystemPrompt, model, cacheRetention, options.Env)
		if err != nil {
			fail(err)
			return
		}
		toolConfig, err := convertBedrockToolConfig(utils.GetCurrentTools(normalizedContext.Messages), options.ToolChoice, supportsStrictMode)
		if err != nil {
			fail(err)
			return
		}
		additionalFields, err := buildAdditionalModelRequestFields(model, options)
		if err != nil {
			fail(err)
			return
		}

		inference := &bedrocktypes.InferenceConfiguration{}
		if inferenceMaxTokens != nil {
			inference.MaxTokens = aws.Int32(int32(*inferenceMaxTokens))
		}
		if options.Temperature != nil {
			inference.Temperature = aws.Float32(float32(*options.Temperature))
		}

		input := &bedrockruntime.ConverseStreamInput{
			ModelId:         aws.String(model.Id),
			Messages:        messages,
			System:          system,
			InferenceConfig: inference,
			ToolConfig:      toolConfig,
		}
		if additionalFields != nil {
			input.AdditionalModelRequestFields = document.NewLazyDocument(additionalFields)
		}
		if options.RequestMetadata != nil {
			input.RequestMetadata = options.RequestMetadata
		}

		if options.OnPayload != nil {
			next, payloadErr := options.OnPayload(input, model)
			if payloadErr != nil {
				fail(payloadErr)
				return
			}
			if next != nil {
				if replacement, ok := next.(*bedrockruntime.ConverseStreamInput); ok {
					input = replacement
				}
			}
		}

		response, err := client.ConverseStream(ctx, input)
		if err != nil {
			fail(err)
			return
		}

		if requestID, ok := awsmiddleware.GetRequestIDMetadata(response.ResultMetadata); ok {
			state.responseRequestId = normalizeBedrockDiagnosticValue(requestID)
		}
		statusCode := bedrockResponseStatusCode(response.ResultMetadata)
		if !observedRawResponse && statusCode != nil {
			headers := map[string]string{}
			if state.responseRequestId != nil {
				headers["x-amzn-requestid"] = *state.responseRequestId
			}
			if options.OnResponse != nil {
				options.OnResponse(types.ProviderResponse{Status: *statusCode, Headers: headers}, model)
			}
		}

		converseStream := response.GetStream()
		if converseStream == nil {
			fail(fmt.Errorf("Bedrock stream ended without a stop reason"))
			return
		}
		for event := range converseStream.Events() {
			if options.OnProviderStreamEvent != nil {
				// Observe the parsed SDK event before normalization. Bedrock's decoder may
				// already discard unknown fields; the observer sees what it produced.
				if observeErr := options.OnProviderStreamEvent(event, model); observeErr != nil {
					fail(observeErr)
					return
				}
			}
			switch item := event.(type) {
			case *bedrocktypes.ConverseStreamOutputMemberMessageStart:
				if item.Value.Role != bedrocktypes.ConversationRoleAssistant {
					fail(fmt.Errorf("Unexpected assistant message start but got user message start instead"))
					return
				}
				stream.Push(types.NewStartEvent(output))
			case *bedrocktypes.ConverseStreamOutputMemberContentBlockStart:
				state.handleContentBlockStart(item.Value, stream)
			case *bedrocktypes.ConverseStreamOutputMemberContentBlockDelta:
				state.handleContentBlockDelta(item.Value, stream)
			case *bedrocktypes.ConverseStreamOutputMemberContentBlockStop:
				state.handleContentBlockStop(item.Value, stream)
			case *bedrocktypes.ConverseStreamOutputMemberMessageStop:
				reason := string(item.Value.StopReason)
				output.RawStopReason = aws.String(reason)
				mapped, errorMessage := mapBedrockStopReason(reason)
				output.StopReason = mapped
				if errorMessage != nil {
					output.ErrorMessage = errorMessage
				}
			case *bedrocktypes.ConverseStreamOutputMemberMetadata:
				handleBedrockMetadata(item.Value, model, &output)
			}
		}
		if streamErr := converseStream.Err(); streamErr != nil {
			fail(streamErr)
			return
		}

		if aborted(signal) {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == types.StopReasonPending {
			fail(fmt.Errorf("Bedrock stream ended without a stop reason"))
			return
		}
		if output.StopReason == types.StopReasonError || output.StopReason == types.StopReasonAborted {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			fail(fmt.Errorf("%s", message))
			return
		}

		state.finalizeAll()
		stream.Push(types.NewDoneEvent(output.StopReason, output))
		stream.End(&output)
	}()

	return stream
}

// BedrockConverseStreamSimple is the unified-reasoning entry point for the
// Bedrock ConverseStream API.
func BedrockConverseStreamSimple(model *types.Model, transcript *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	base := BuildBaseOptions(model, transcript, options, nil)
	typed := &BedrockOptions{StreamOptions: base}
	if options != nil && options.ToolChoice != nil {
		typed.ToolChoice = string(*options.ToolChoice)
	}
	if options == nil || options.Reasoning == nil {
		return BedrockConverseStream(model, transcript, typed)
	}

	reasoning := *options.Reasoning
	typed.Reasoning = &reasoning
	typed.ThinkingBudgets = options.ThinkingBudgets

	if !isAnthropicClaudeModel(model) || supportsAdaptiveThinking(model.Id, &model.Name) {
		return BedrockConverseStream(model, transcript, typed)
	}

	var baseMaxTokens *float64
	if base.MaxTokens != nil {
		value := float64(*base.MaxTokens)
		baseMaxTokens = &value
	}
	adjusted := AdjustMaxTokensForThinking(baseMaxTokens, model.MaxTokens, reasoning, options.ThinkingBudgets)
	maxTokens := ClampMaxTokensToContext(model, transcript, float64(adjusted.MaxTokens))
	maxTokensInt := int(maxTokens)
	typed.MaxTokens = &maxTokensInt

	budgets := types.ThinkingBudgets{}
	if options.ThinkingBudgets != nil {
		budgets = *options.ThinkingBudgets
	}
	if clamped := ClampReasoning(&reasoning); clamped != nil {
		room := maxTokensInt - MinAnswerTokens
		if room < 0 {
			room = 0
		}
		budget := adjusted.ThinkingBudget
		if budget > room {
			budget = room
		}
		setBedrockThinkingBudget(&budgets, *clamped, budget)
	}
	typed.ThinkingBudgets = &budgets

	return BedrockConverseStream(model, transcript, typed)
}

// =============================================================================
// Stream state and block handling
// =============================================================================

type bedrockStreamState struct {
	output            *types.AssistantMessage
	positions         map[int]int
	partialJSON       map[int]string
	redactedChunks    map[int][][]byte
	responseRequestId *string
}

func (s *bedrockStreamState) block(contentBlockIndex int) (*types.ContentBlock, bool) {
	position, ok := s.positions[contentBlockIndex]
	if !ok || position < 0 || position >= len(s.output.Content) {
		return nil, false
	}
	return &s.output.Content[position], true
}

func (s *bedrockStreamState) handleContentBlockStart(event bedrocktypes.ContentBlockStartEvent, stream *types.AssistantMessageEventStream) {
	index := bedrockInt32Index(event.ContentBlockIndex)
	start, ok := event.Start.(*bedrocktypes.ContentBlockStartMemberToolUse)
	if !ok {
		return
	}
	id := ""
	if start.Value.ToolUseId != nil {
		id = *start.Value.ToolUseId
	}
	name := ""
	if start.Value.Name != nil {
		name = *start.Value.Name
	}
	toolCall := types.NewToolCall(id, name, json.RawMessage("{}"))
	s.output.Content = append(s.output.Content, types.ToolCallBlock(toolCall))
	s.positions[index] = len(s.output.Content) - 1
	s.partialJSON[index] = ""
	stream.Push(types.NewToolCallStartEvent(len(s.output.Content)-1, *s.output))
}

func (s *bedrockStreamState) handleContentBlockDelta(event bedrocktypes.ContentBlockDeltaEvent, stream *types.AssistantMessageEventStream) {
	contentBlockIndex := bedrockInt32Index(event.ContentBlockIndex)
	delta := event.Delta

	switch value := delta.(type) {
	case *bedrocktypes.ContentBlockDeltaMemberText:
		block, ok := s.block(contentBlockIndex)
		if !ok {
			s.output.Content = append(s.output.Content, types.TextBlock(""))
			position := len(s.output.Content) - 1
			s.positions[contentBlockIndex] = position
			stream.Push(types.NewTextStartEvent(position, *s.output))
			block, _ = s.block(contentBlockIndex)
		}
		if block != nil && block.Type == types.ContentTypeText && block.Text != nil {
			block.Text.Text += value.Value
			stream.Push(types.NewTextDeltaEvent(s.positions[contentBlockIndex], value.Value, *s.output))
		}
	case *bedrocktypes.ContentBlockDeltaMemberToolUse:
		block, ok := s.block(contentBlockIndex)
		if !ok || block.Type != types.ContentTypeToolCall || block.ToolCall == nil {
			return
		}
		input := ""
		if value.Value.Input != nil {
			input = *value.Value.Input
		}
		partial := s.partialJSON[contentBlockIndex] + input
		s.partialJSON[contentBlockIndex] = partial
		block.ToolCall.Arguments = bedrockArgumentsFromAny(utils.ParseStreamingJSON(partial))
		stream.Push(types.NewToolCallDeltaEvent(s.positions[contentBlockIndex], input, *s.output))
	case *bedrocktypes.ContentBlockDeltaMemberReasoningContent:
		s.handleReasoningDelta(contentBlockIndex, value.Value, stream)
	}
}

func (s *bedrockStreamState) handleReasoningDelta(contentBlockIndex int, delta bedrocktypes.ReasoningContentBlockDelta, stream *types.AssistantMessageEventStream) {
	block, ok := s.block(contentBlockIndex)
	if !ok {
		newBlock := types.ThinkingBlock("")
		if newBlock.Thinking != nil {
			empty := ""
			newBlock.Thinking.ThinkingSignature = &empty
		}
		s.output.Content = append(s.output.Content, newBlock)
		position := len(s.output.Content) - 1
		s.positions[contentBlockIndex] = position
		stream.Push(types.NewThinkingStartEvent(position, *s.output))
		block, _ = s.block(contentBlockIndex)
	}
	if block == nil || block.Type != types.ContentTypeThinking || block.Thinking == nil {
		return
	}
	thinking := block.Thinking
	redacted := thinking.Redacted != nil && *thinking.Redacted

	switch value := delta.(type) {
	case *bedrocktypes.ReasoningContentBlockDeltaMemberText:
		if value.Value != "" {
			thinking.Thinking += value.Value
			stream.Push(types.NewThinkingDeltaEvent(s.positions[contentBlockIndex], value.Value, *s.output))
		}
	case *bedrocktypes.ReasoningContentBlockDeltaMemberSignature:
		if value.Value != "" && !redacted {
			current := ""
			if thinking.ThinkingSignature != nil {
				current = *thinking.ThinkingSignature
			}
			signature := current + value.Value
			thinking.ThinkingSignature = &signature
		}
	case *bedrocktypes.ReasoningContentBlockDeltaMemberRedactedContent:
		chunk := append([]byte(nil), value.Value...)
		if len(chunk) > 0 {
			if !redacted {
				flag := true
				thinking.Redacted = &flag
				empty := ""
				thinking.ThinkingSignature = &empty
				thinking.Thinking += bedrockRedactedThinkingPlaceholder
				stream.Push(types.NewThinkingDeltaEvent(s.positions[contentBlockIndex], bedrockRedactedThinkingPlaceholder, *s.output))
			}
			s.redactedChunks[contentBlockIndex] = append(s.redactedChunks[contentBlockIndex], chunk)
		}
	}
}

func (s *bedrockStreamState) handleContentBlockStop(event bedrocktypes.ContentBlockStopEvent, stream *types.AssistantMessageEventStream) {
	contentBlockIndex := bedrockInt32Index(event.ContentBlockIndex)
	position, ok := s.positions[contentBlockIndex]
	if !ok {
		return
	}
	delete(s.positions, contentBlockIndex)
	if position < 0 || position >= len(s.output.Content) {
		return
	}
	block := &s.output.Content[position]
	switch block.Type {
	case types.ContentTypeText:
		text := ""
		if block.Text != nil {
			text = block.Text.Text
		}
		stream.Push(types.NewTextEndEvent(position, text, *s.output))
	case types.ContentTypeThinking:
		s.flushRedactedContent(contentBlockIndex, block)
		text := ""
		if block.Thinking != nil {
			text = block.Thinking.Thinking
		}
		stream.Push(types.NewThinkingEndEvent(position, text, *s.output))
	case types.ContentTypeToolCall:
		if block.ToolCall != nil {
			block.ToolCall.Arguments = bedrockArgumentsFromAny(utils.ParseStreamingJSON(s.partialJSON[contentBlockIndex]))
			stream.Push(types.NewToolCallEndEvent(position, *block.ToolCall, *s.output))
		}
	}
	delete(s.partialJSON, contentBlockIndex)
}

func (s *bedrockStreamState) flushRedactedContent(contentBlockIndex int, block *types.ContentBlock) {
	chunks := s.redactedChunks[contentBlockIndex]
	if block.Type != types.ContentTypeThinking || block.Thinking == nil || chunks == nil {
		return
	}
	signature := bedrockBytesToBase64(chunks)
	block.Thinking.ThinkingSignature = &signature
	delete(s.redactedChunks, contentBlockIndex)
}

func (s *bedrockStreamState) finalizeAll() {
	for contentBlockIndex, chunks := range s.redactedChunks {
		position, ok := s.positions[contentBlockIndex]
		if !ok || position < 0 || position >= len(s.output.Content) {
			continue
		}
		block := &s.output.Content[position]
		if block.Type == types.ContentTypeThinking && block.Thinking != nil {
			signature := bedrockBytesToBase64(chunks)
			block.Thinking.ThinkingSignature = &signature
		}
	}
	s.redactedChunks = map[int][][]byte{}
	s.partialJSON = map[int]string{}
	s.positions = map[int]int{}
}

// =============================================================================
// Response conversion
// =============================================================================

func handleBedrockMetadata(event bedrocktypes.ConverseStreamMetadataEvent, model *types.Model, output *types.AssistantMessage) {
	if event.Usage == nil {
		return
	}
	usage := event.Usage
	output.Usage.Input = bedrockInt32Value(usage.InputTokens)
	output.Usage.Output = bedrockInt32Value(usage.OutputTokens)
	output.Usage.CacheRead = bedrockInt32Value(usage.CacheReadInputTokens)
	output.Usage.CacheWrite = bedrockInt32Value(usage.CacheWriteInputTokens)
	if usage.CacheDetails != nil {
		total := 0.0
		for _, detail := range usage.CacheDetails {
			if detail.Ttl == bedrocktypes.CacheTTLOneHour {
				total += bedrockInt32Value(detail.InputTokens)
			}
		}
		output.Usage.CacheWrite1h = &total
	} else {
		output.Usage.CacheWrite1h = nil
	}
	totalTokens := bedrockInt32Value(usage.TotalTokens)
	if totalTokens == 0 {
		totalTokens = output.Usage.Input + output.Usage.Output
	}
	output.Usage.TotalTokens = totalTokens
	calculateCost(model, &output.Usage)
}

func mapBedrockStopReason(reason string) (types.StopReason, *string) {
	switch reason {
	case string(bedrocktypes.StopReasonEndTurn), string(bedrocktypes.StopReasonStopSequence):
		return types.StopReasonStop, nil
	case string(bedrocktypes.StopReasonMaxTokens), string(bedrocktypes.StopReasonModelContextWindowExceeded):
		return types.StopReasonLength, nil
	case string(bedrocktypes.StopReasonToolUse):
		return types.StopReasonToolUse, nil
	default:
		if reason != "" {
			message := "Provider stopped with: " + reason
			return types.StopReasonError, &message
		}
		return types.StopReasonError, nil
	}
}

// =============================================================================
// Request conversion
// =============================================================================

func buildBedrockSystemPrompt(systemPrompt *string, model *types.Model, cacheRetention types.CacheRetention, env types.ProviderEnv) ([]bedrocktypes.SystemContentBlock, error) {
	if systemPrompt == nil || *systemPrompt == "" {
		return nil, nil
	}
	text := utils.SanitizeSurrogates(*systemPrompt)
	blocks := []bedrocktypes.SystemContentBlock{
		&bedrocktypes.SystemContentBlockMemberText{Value: text},
	}
	if cacheRetention != types.CacheRetentionNone && supportsBedrockPromptCaching(model, env) {
		blocks = append(blocks, &bedrocktypes.SystemContentBlockMemberCachePoint{Value: bedrockCachePoint(cacheRetention)})
	}
	return blocks, nil
}

func bedrockCachePoint(cacheRetention types.CacheRetention) bedrocktypes.CachePointBlock {
	point := bedrocktypes.CachePointBlock{Type: bedrocktypes.CachePointTypeDefault}
	if cacheRetention == types.CacheRetentionLong {
		point.Ttl = bedrocktypes.CacheTTLOneHour
	}
	return point
}

func normalizeBedrockToolCallID(id string, model *types.Model, source types.AssistantMessage) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, id)
	if len(sanitized) > 64 {
		return sanitized[:64]
	}
	return sanitized
}

func createBedrockNonBlankTextBlock(text string) *bedrocktypes.ContentBlockMemberText {
	sanitized := utils.SanitizeSurrogates(text)
	if isBlank(sanitized) {
		return nil
	}
	return &bedrocktypes.ContentBlockMemberText{Value: sanitized}
}

func createBedrockRequiredTextBlock(text string) bedrocktypes.ContentBlock {
	if block := createBedrockNonBlankTextBlock(text); block != nil {
		return block
	}
	return &bedrocktypes.ContentBlockMemberText{Value: bedrockEmptyTextPlaceholder}
}

func sanitizeBedrockDocument(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = sanitizeBedrockDocument(entry)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, entry := range typed {
			if key == "" {
				continue
			}
			out[key] = sanitizeBedrockDocument(entry)
		}
		return out
	case json.Number:
		return smithydocument.Number(string(typed))
	default:
		return value
	}
}

func decodeBedrockDocument(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}

func bedrockDocumentFromRaw(raw json.RawMessage) document.Interface {
	return document.NewLazyDocument(sanitizeBedrockDocument(decodeBedrockDocument(raw)))
}

func bedrockDocumentFromAny(value any) document.Interface {
	return document.NewLazyDocument(sanitizeBedrockDocument(value))
}

func convertBedrockToolResultContent(content []types.ContentBlock) []bedrocktypes.ToolResultContentBlock {
	result := []bedrocktypes.ToolResultContentBlock{}
	for _, block := range content {
		if block.Type == types.ContentTypeImage && block.Image != nil {
			image, err := createBedrockImageBlock(block.Image.MimeType, block.Image.Data)
			if err != nil {
				continue
			}
			result = append(result, &bedrocktypes.ToolResultContentBlockMemberImage{Value: image})
			continue
		}
		if block.Type == types.ContentTypeText && block.Text != nil {
			if textBlock := createBedrockNonBlankTextBlock(block.Text.Text); textBlock != nil {
				result = append(result, &bedrocktypes.ToolResultContentBlockMemberText{Value: textBlock.Value})
			}
		}
	}
	if len(result) == 0 {
		result = append(result, &bedrocktypes.ToolResultContentBlockMemberText{Value: bedrockEmptyTextPlaceholder})
	}
	return result
}

func convertBedrockMessages(context types.TranscriptContext, model *types.Model, cacheRetention types.CacheRetention, env types.ProviderEnv) ([]bedrocktypes.Message, error) {
	result := []bedrocktypes.Message{}
	transformed := TransformMessages(utils.WithoutInitialSystemMessage(context.Messages), model, normalizeBedrockToolCallID)

	for i := 0; i < len(transformed); i++ {
		message := transformed[i]
		switch message.Role {
		case types.UserMessageRole:
			if message.User == nil {
				continue
			}
			content := []bedrocktypes.ContentBlock{}
			if !message.User.Content.Structured {
				content = append(content, createBedrockRequiredTextBlock(message.User.Content.Text))
			} else {
				for _, block := range message.User.Content.Blocks {
					switch block.Type {
					case types.ContentTypeText:
						if block.Text != nil {
							if textBlock := createBedrockNonBlankTextBlock(block.Text.Text); textBlock != nil {
								content = append(content, textBlock)
							}
						}
					case types.ContentTypeImage:
						if block.Image != nil {
							image, err := createBedrockImageBlock(block.Image.MimeType, block.Image.Data)
							if err != nil {
								return nil, err
							}
							content = append(content, &bedrocktypes.ContentBlockMemberImage{Value: image})
						}
					}
				}
				if len(content) == 0 {
					content = append(content, &bedrocktypes.ContentBlockMemberText{Value: bedrockEmptyTextPlaceholder})
				}
			}
			result = append(result, bedrocktypes.Message{Role: bedrocktypes.ConversationRoleUser, Content: content})

		case types.AssistantMessageRole:
			if message.Assistant == nil || len(message.Assistant.Content) == 0 {
				continue
			}
			content := []bedrocktypes.ContentBlock{}
			for _, block := range message.Assistant.Content {
				switch block.Type {
				case types.ContentTypeText:
					if block.Text == nil {
						continue
					}
					if textBlock := createBedrockNonBlankTextBlock(block.Text.Text); textBlock != nil {
						content = append(content, textBlock)
					}
				case types.ContentTypeToolCall:
					if block.ToolCall == nil {
						continue
					}
					content = append(content, &bedrocktypes.ContentBlockMemberToolUse{Value: bedrocktypes.ToolUseBlock{
						ToolUseId: aws.String(block.ToolCall.Id),
						Name:      aws.String(block.ToolCall.Name),
						Input:     bedrockDocumentFromRaw(block.ToolCall.Arguments),
					}})
				case types.ContentTypeThinking:
					if block.Thinking == nil {
						continue
					}
					if block.Thinking.Redacted != nil && *block.Thinking.Redacted {
						payload := decodeBedrockRedactedContent(block.Thinking.ThinkingSignature)
						if len(payload) > 0 {
							content = append(content, &bedrocktypes.ContentBlockMemberReasoningContent{
								Value: &bedrocktypes.ReasoningContentBlockMemberRedactedContent{Value: payload},
							})
						}
						continue
					}
					thinking := utils.SanitizeSurrogates(block.Thinking.Thinking)
					if isBlank(thinking) {
						continue
					}
					if supportsBedrockThinkingSignature(model) {
						signature := ""
						if block.Thinking.ThinkingSignature != nil {
							signature = strings.TrimSpace(*block.Thinking.ThinkingSignature)
						}
						if signature == "" {
							content = append(content, &bedrocktypes.ContentBlockMemberText{Value: thinking})
						} else {
							content = append(content, &bedrocktypes.ContentBlockMemberReasoningContent{
								Value: &bedrocktypes.ReasoningContentBlockMemberReasoningText{
									Value: bedrocktypes.ReasoningTextBlock{Text: aws.String(thinking), Signature: aws.String(*block.Thinking.ThinkingSignature)},
								},
							})
						}
					} else {
						content = append(content, &bedrocktypes.ContentBlockMemberReasoningContent{
							Value: &bedrocktypes.ReasoningContentBlockMemberReasoningText{
								Value: bedrocktypes.ReasoningTextBlock{Text: aws.String(thinking)},
							},
						})
					}
				}
			}
			if len(content) == 0 {
				continue
			}
			result = append(result, bedrocktypes.Message{Role: bedrocktypes.ConversationRoleAssistant, Content: content})

		case types.ToolResultMessageRole:
			if message.ToolResult == nil {
				continue
			}
			toolResults := []bedrocktypes.ContentBlock{
				&bedrocktypes.ContentBlockMemberToolResult{Value: bedrocktypes.ToolResultBlock{
					ToolUseId: aws.String(message.ToolResult.ToolCallId),
					Content:   convertBedrockToolResultContent(message.ToolResult.Content),
					Status:    bedrockToolResultStatus(message.ToolResult.IsError),
				}},
			}
			j := i + 1
			for j < len(transformed) && transformed[j].Role == types.ToolResultMessageRole {
				next := transformed[j].ToolResult
				if next != nil {
					toolResults = append(toolResults, &bedrocktypes.ContentBlockMemberToolResult{Value: bedrocktypes.ToolResultBlock{
						ToolUseId: aws.String(next.ToolCallId),
						Content:   convertBedrockToolResultContent(next.Content),
						Status:    bedrockToolResultStatus(next.IsError),
					}})
				}
				j++
			}
			i = j - 1
			result = append(result, bedrocktypes.Message{Role: bedrocktypes.ConversationRoleUser, Content: toolResults})
		}
	}

	if cacheRetention != types.CacheRetentionNone && supportsBedrockPromptCaching(model, env) && len(result) > 0 {
		last := &result[len(result)-1]
		if last.Role == bedrocktypes.ConversationRoleUser && len(last.Content) > 0 {
			last.Content = append(last.Content, &bedrocktypes.ContentBlockMemberCachePoint{Value: bedrockCachePoint(cacheRetention)})
		}
	}

	return result, nil
}

func bedrockToolResultStatus(isError bool) bedrocktypes.ToolResultStatus {
	if isError {
		return bedrocktypes.ToolResultStatusError
	}
	return bedrocktypes.ToolResultStatusSuccess
}

func convertBedrockToolConfig(tools []types.Tool, toolChoice any, supportsStrictMode bool) (*bedrocktypes.ToolConfiguration, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	if choice, ok := toolChoice.(string); ok && choice == "none" {
		return nil, nil
	}

	bedrockTools := make([]bedrocktypes.Tool, 0, len(tools))
	for _, tool := range tools {
		strict, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		parameters, err := GetJSONSchemaToolParameters(tool, strict)
		if err != nil {
			return nil, err
		}
		specification := bedrocktypes.ToolSpecification{
			Name:        aws.String(tool.Name),
			Description: aws.String(tool.Description),
			InputSchema: &bedrocktypes.ToolInputSchemaMemberJson{Value: bedrockDocumentFromRaw(parameters)},
		}
		if strict != nil && *strict {
			specification.Strict = aws.Bool(true)
		}
		bedrockTools = append(bedrockTools, &bedrocktypes.ToolMemberToolSpec{Value: specification})
	}

	var bedrockToolChoice bedrocktypes.ToolChoice
	switch value := toolChoice.(type) {
	case string:
		switch value {
		case "auto":
			bedrockToolChoice = &bedrocktypes.ToolChoiceMemberAuto{Value: bedrocktypes.AutoToolChoice{}}
		case "any":
			bedrockToolChoice = &bedrocktypes.ToolChoiceMemberAny{Value: bedrocktypes.AnyToolChoice{}}
		}
	case BedrockToolChoice:
		if value.Type == "tool" {
			bedrockToolChoice = &bedrocktypes.ToolChoiceMemberTool{Value: bedrocktypes.SpecificToolChoice{Name: aws.String(value.Name)}}
		}
	case *BedrockToolChoice:
		if value != nil && value.Type == "tool" {
			bedrockToolChoice = &bedrocktypes.ToolChoiceMemberTool{Value: bedrocktypes.SpecificToolChoice{Name: aws.String(value.Name)}}
		}
	}

	return &bedrocktypes.ToolConfiguration{Tools: bedrockTools, ToolChoice: bedrockToolChoice}, nil
}

func createBedrockImageBlock(mimeType, data string) (bedrocktypes.ImageBlock, error) {
	var format bedrocktypes.ImageFormat
	switch mimeType {
	case "image/jpeg", "image/jpg":
		format = bedrocktypes.ImageFormatJpeg
	case "image/png":
		format = bedrocktypes.ImageFormatPng
	case "image/gif":
		format = bedrocktypes.ImageFormatGif
	case "image/webp":
		format = bedrocktypes.ImageFormatWebp
	default:
		return bedrocktypes.ImageBlock{}, fmt.Errorf("Unknown image type: %s", mimeType)
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return bedrocktypes.ImageBlock{}, fmt.Errorf("Invalid base64 image data: %w", err)
	}
	return bedrocktypes.ImageBlock{
		Format: format,
		Source: &bedrocktypes.ImageSourceMemberBytes{Value: bytes},
	}, nil
}

func decodeBedrockRedactedContent(signature *string) []byte {
	if signature == nil || *signature == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(*signature)
	if err != nil {
		return nil
	}
	return decoded
}

func bedrockBytesToBase64(chunks [][]byte) string {
	var buffer bytes.Buffer
	for _, chunk := range chunks {
		buffer.Write(chunk)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func bedrockArgumentsFromAny(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

// =============================================================================
// Model helpers
// =============================================================================

func getBedrockModelMatchCandidates(modelID string, modelName *string) []string {
	values := []string{modelID}
	if modelName != nil && *modelName != "" {
		values = append(values, *modelName)
	}
	out := []string{}
	for _, value := range values {
		lower := strings.ToLower(value)
		out = append(out, lower, bedrockWhitespaceSep.ReplaceAllString(lower, "-"))
	}
	return out
}

func supportsAdaptiveThinking(modelID string, modelName *string) bool {
	for _, candidate := range getBedrockModelMatchCandidates(modelID, modelName) {
		for _, marker := range []string{"opus-4-6", "opus-4-7", "opus-4-8", "opus-5", "sonnet-4-6", "sonnet-5", "fable-5"} {
			if strings.Contains(candidate, marker) {
				return true
			}
		}
	}
	return false
}

func supportsNativeXhighEffort(model *types.Model) bool {
	for _, candidate := range getBedrockModelMatchCandidates(model.Id, &model.Name) {
		for _, marker := range []string{"opus-4-7", "opus-4-8", "opus-5", "sonnet-5", "fable-5"} {
			if strings.Contains(candidate, marker) {
				return true
			}
		}
	}
	return false
}

func mapBedrockThinkingLevelToEffort(model *types.Model, level *types.ThinkingLevel) string {
	if level != nil && *level == types.ThinkingXHigh && supportsNativeXhighEffort(model) {
		return "xhigh"
	}
	if level != nil {
		if mapped, present, supported := model.ThinkingLevelMap.Lookup(types.ModelThinkingLevel(*level)); present && supported {
			return mapped
		}
	}
	if level == nil {
		return "high"
	}
	switch *level {
	case types.ThinkingMinimal, types.ThinkingLow:
		return "low"
	case types.ThinkingMedium:
		return "medium"
	case types.ThinkingHigh:
		return "high"
	default:
		return "high"
	}
}

func isAnthropicClaudeModel(model *types.Model) bool {
	id := strings.ToLower(model.Id)
	name := strings.ToLower(model.Name)
	return strings.Contains(id, "anthropic.claude") ||
		strings.Contains(id, "anthropic/claude") ||
		strings.Contains(name, "anthropic.claude") ||
		strings.Contains(name, "anthropic/claude") ||
		strings.Contains(name, "claude")
}

func supportsBedrockPromptCaching(model *types.Model, env types.ProviderEnv) bool {
	candidates := getBedrockModelMatchCandidates(model.Id, &model.Name)
	hasClaudeRef := false
	for _, candidate := range candidates {
		if strings.Contains(candidate, "claude") {
			hasClaudeRef = true
			break
		}
	}
	if !hasClaudeRef {
		if value := utils.GetProviderEnvValue("AWS_BEDROCK_FORCE_CACHE", env); value != nil && *value == "1" {
			return true
		}
		return false
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "fable-5") || strings.Contains(candidate, "opus-5") || strings.Contains(candidate, "sonnet-5") {
			return true
		}
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "-4-") {
			return true
		}
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "claude-3-7-sonnet") {
			return true
		}
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "claude-3-5-haiku") {
			return true
		}
	}
	return false
}

func supportsBedrockThinkingSignature(model *types.Model) bool {
	return isAnthropicClaudeModel(model)
}

func buildAdditionalModelRequestFields(model *types.Model, options *BedrockOptions) (map[string]any, error) {
	if options.Reasoning == nil || !model.Reasoning {
		return nil, nil
	}
	if !isAnthropicClaudeModel(model) {
		return nil, nil
	}

	var display *string
	if !isGovCloudBedrockTarget(model, options) {
		value := string(BedrockThinkingDisplaySummarized)
		if options.ThinkingDisplay != nil {
			value = string(*options.ThinkingDisplay)
		}
		display = &value
	}

	if supportsAdaptiveThinking(model.Id, &model.Name) {
		result := map[string]any{
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": mapBedrockThinkingLevelToEffort(model, options.Reasoning)},
		}
		if display != nil {
			result["thinking"].(map[string]any)["display"] = *display
		}
		return result, nil
	}

	defaultBudgets := map[types.ThinkingLevel]int{
		types.ThinkingMinimal: 1024,
		types.ThinkingLow:     2048,
		types.ThinkingMedium:  8192,
		types.ThinkingHigh:    16384,
		types.ThinkingXHigh:   16384,
		types.ThinkingMax:     16384,
	}
	level := *options.Reasoning
	if level == types.ThinkingXHigh || level == types.ThinkingMax {
		level = types.ThinkingHigh
	}
	budget := defaultBudgets[*options.Reasoning]
	if options.ThinkingBudgets != nil {
		if override, ok := bedrockThinkingBudgetForLevel(options.ThinkingBudgets, level); ok {
			budget = override
		}
	}
	thinking := map[string]any{
		"type":          "enabled",
		"budget_tokens": budget,
	}
	if display != nil {
		thinking["display"] = *display
	}
	result := map[string]any{"thinking": thinking}

	interleaved := true
	if options.InterleavedThinking != nil {
		interleaved = *options.InterleavedThinking
	}
	if interleaved {
		result["anthropic_beta"] = []string{bedrockInterleavedThinkingBetaValue}
	}
	return result, nil
}

func bedrockThinkingBudgetForLevel(budgets *types.ThinkingBudgets, level types.ThinkingLevel) (int, bool) {
	if budgets == nil {
		return 0, false
	}
	switch level {
	case types.ThinkingMinimal:
		if budgets.Minimal != nil {
			return *budgets.Minimal, true
		}
	case types.ThinkingLow:
		if budgets.Low != nil {
			return *budgets.Low, true
		}
	case types.ThinkingMedium:
		if budgets.Medium != nil {
			return *budgets.Medium, true
		}
	case types.ThinkingHigh:
		if budgets.High != nil {
			return *budgets.High, true
		}
	}
	return 0, false
}

func setBedrockThinkingBudget(budgets *types.ThinkingBudgets, level types.ThinkingLevel, value int) {
	if budgets == nil {
		return
	}
	switch level {
	case types.ThinkingMinimal:
		budgets.Minimal = &value
	case types.ThinkingLow:
		budgets.Low = &value
	case types.ThinkingMedium:
		budgets.Medium = &value
	case types.ThinkingHigh:
		budgets.High = &value
	}
}

func isGovCloudBedrockTarget(model *types.Model, options *BedrockOptions) bool {
	region := getConfiguredBedrockRegion(options)
	if region != "" && strings.HasPrefix(strings.ToLower(region), "us-gov-") {
		return true
	}
	modelID := strings.ToLower(model.Id)
	return strings.HasPrefix(modelID, "us-gov.") || strings.HasPrefix(modelID, "arn:aws-us-gov:")
}

// =============================================================================
// AWS configuration
// =============================================================================

func getConfiguredBedrockRegion(options *BedrockOptions) string {
	if options.Region != nil && *options.Region != "" {
		return *options.Region
	}
	if value := utils.GetProviderEnvValue("AWS_REGION", options.Env); value != nil && *value != "" {
		return *value
	}
	if value := utils.GetProviderEnvValue("AWS_DEFAULT_REGION", options.Env); value != nil && *value != "" {
		return *value
	}
	return ""
}

func getStandardBedrockEndpointRegion(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	match := bedrockEndpointPattern.FindStringSubmatch(strings.ToLower(parsed.Hostname()))
	if match == nil {
		return ""
	}
	return match[1]
}

func shouldUseExplicitBedrockEndpoint(baseURL, configuredRegion string, hasAmbientConfiguredProfile bool) bool {
	endpointRegion := getStandardBedrockEndpointRegion(baseURL)
	if endpointRegion == "" {
		return true
	}
	return configuredRegion == "" && !hasAmbientConfiguredProfile
}

func resolveBedrockAWSConfig(ctx context.Context, model *types.Model, options *BedrockOptions, observedRawResponse *bool) (aws.Config, error) {
	env := options.Env

	profile := ""
	if options.Profile != nil {
		profile = *options.Profile
	}
	if profile == "" {
		if value := utils.GetProviderEnvValue("AWS_PROFILE", env); value != nil {
			profile = *value
		}
	}
	configuredRegion := getConfiguredBedrockRegion(options)
	hasAmbientConfiguredProfile := utils.GetProviderEnvValue("AWS_PROFILE", nil) != nil
	endpointRegion := getStandardBedrockEndpointRegion(model.BaseUrl)
	useExplicitEndpoint := shouldUseExplicitBedrockEndpoint(model.BaseUrl, configuredRegion, hasAmbientConfiguredProfile)

	region := ""
	if match := bedrockARNRegionPattern.FindStringSubmatch(model.Id); match != nil {
		region = match[1]
	} else if configuredRegion != "" {
		region = configuredRegion
	} else if endpointRegion != "" && useExplicitEndpoint {
		region = endpointRegion
	} else if !hasAmbientConfiguredProfile {
		region = "us-east-1"
	}
	if region == "" {
		region = "us-east-1"
	}

	skipAuth := false
	if value := utils.GetProviderEnvValue("AWS_BEDROCK_SKIP_AUTH", env); value != nil && *value == "1" {
		skipAuth = true
	}

	bearerToken := ""
	if options.BearerToken != nil && *options.BearerToken != "" {
		bearerToken = *options.BearerToken
	} else if options.APIKey != nil && *options.APIKey != "" {
		bearerToken = *options.APIKey
	} else if value := utils.GetProviderEnvValue("AWS_BEARER_TOKEN_BEDROCK", env); value != nil {
		bearerToken = *value
	}
	useBearerToken := bearerToken != "" && !skipAuth

	accessKeyID := utils.GetProviderEnvValue("AWS_ACCESS_KEY_ID", env)
	secretAccessKey := utils.GetProviderEnvValue("AWS_SECRET_ACCESS_KEY", env)
	hasExplicitCredentials := accessKeyID != nil && *accessKeyID != "" && secretAccessKey != nil && *secretAccessKey != ""

	var cfg aws.Config
	if useBearerToken || skipAuth || (hasExplicitCredentials && profile == "") {
		cfg = aws.Config{Region: region}
	} else {
		loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
		if profile != "" {
			loadOptions = append(loadOptions, awsconfig.WithSharedConfigProfile(profile))
		}
		loaded, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
		if err != nil {
			return aws.Config{}, err
		}
		cfg = loaded
	}
	cfg.Region = region

	if useExplicitEndpoint {
		cfg.BaseEndpoint = aws.String(model.BaseUrl)
	}
	if useBearerToken {
		// The smithy bearer signer refuses plain-HTTP endpoints, while the
		// upstream JS SDK sends the Authorization header to custom endpoints
		// over HTTP. Select the anonymous scheme and inject the bearer header
		// ourselves so the observable wire behavior (a bearer Authorization
		// header) is preserved even against a local/HTTP gateway.
		cfg.AuthSchemePreference = []string{"noAuth"}
		addBedrockBearerTokenMiddleware(&cfg, bearerToken)
	}
	if skipAuth {
		cfg.Credentials = credentials.NewStaticCredentialsProvider("dummy-access-key", "dummy-secret-key", "")
	}
	if hasExplicitCredentials && !skipAuth && profile == "" {
		sessionToken := ""
		if value := utils.GetProviderEnvValue("AWS_SESSION_TOKEN", env); value != nil {
			sessionToken = *value
		}
		cfg.Credentials = credentials.NewStaticCredentialsProvider(*accessKeyID, *secretAccessKey, sessionToken)
	}

	if proxyURL, err := utils.ResolveHttpProxyUrlForTarget(model.BaseUrl, env); err != nil {
		return aws.Config{}, err
	} else if proxyURL != nil {
		cfg.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	}

	if options.OnResponse != nil {
		addBedrockResponseHeadersMiddleware(&cfg, options.OnResponse, model, observedRawResponse)
	}
	if customHeaders := utils.ProviderHeadersToRecord(options.Headers); customHeaders != nil {
		addBedrockCustomHeadersMiddleware(&cfg, customHeaders)
	}

	return cfg, nil
}

// addBedrockBearerTokenMiddleware sets the bearer Authorization header after
// signing. It backs the Bedrock API-key auth path on endpoints where the SDK's
// built-in bearer signer refuses to run (plain HTTP custom endpoints).
func addBedrockBearerTokenMiddleware(cfg *aws.Config, token string) {
	cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
		return stack.Finalize.Add(middleware.FinalizeMiddlewareFunc("pi-ai-bedrock-bearer-token", func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
			if request, ok := in.Request.(*smithyhttp.Request); ok && request.Request != nil {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			return next.HandleFinalize(ctx, in)
		}), middleware.After)
	})
}

func addBedrockCustomHeadersMiddleware(cfg *aws.Config, headers map[string]string) {
	cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
		return stack.Build.Add(middleware.BuildMiddlewareFunc("pi-ai-custom-headers", func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
			if request, ok := in.Request.(*smithyhttp.Request); ok && request.Request != nil {
				for key, value := range headers {
					if !isReservedBedrockHeader(key) {
						request.Header.Set(key, value)
					}
				}
			}
			return next.HandleBuild(ctx, in)
		}), middleware.After)
	})
}

func addBedrockResponseHeadersMiddleware(cfg *aws.Config, onResponse func(types.ProviderResponse, *types.Model), model *types.Model, observedRawResponse *bool) {
	cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
		return stack.Deserialize.Add(middleware.DeserializeMiddlewareFunc("pi-ai-response-headers", func(ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
			out, metadata, err := next.HandleDeserialize(ctx, in)
			if response, ok := out.RawResponse.(*smithyhttp.Response); ok && response != nil {
				if observedRawResponse != nil {
					*observedRawResponse = true
				}
				headers := map[string]string{}
				for key, values := range response.Header {
					if len(values) > 0 {
						headers[key] = values[len(values)-1]
					}
				}
				onResponse(types.ProviderResponse{Status: response.StatusCode, Headers: headers}, model)
			}
			return out, metadata, err
		}), middleware.After)
	})
}

func bedrockResponseStatusCode(metadata middleware.Metadata) *int {
	if raw := awsmiddleware.GetRawResponse(metadata); raw != nil {
		if response, ok := raw.(*smithyhttp.Response); ok && response != nil {
			status := response.StatusCode
			return &status
		}
	}
	return nil
}

// =============================================================================
// Error helpers
// =============================================================================

func formatBedrockError(err error) string {
	norm := utils.NormalizeProviderError(err)
	core := norm.Message
	if !norm.MessageCarriesBody && norm.Status != nil && norm.Body != nil {
		core = fmt.Sprintf("%d: %s", *norm.Status, *norm.Body)
	}
	if bedrockDataRetentionMatch.MatchString(strings.ToLower(core)) {
		core += " See " + bedrockDataRetentionDocsURL + " for supported data retention modes."
	}
	if code := bedrockErrorCode(err); code != "" && strings.HasSuffix(code, "Exception") {
		prefix := code
		if mapped, ok := bedrockErrorPrefixes[code]; ok {
			prefix = mapped
		}
		return prefix + ": " + core
	}
	return core
}

func bedrockErrorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func normalizeBedrockDiagnosticValue(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxBedrockDiagnosticValueChars {
		return nil
	}
	return &trimmed
}

func extractBedrockErrorCode(err error) string {
	code := bedrockErrorCode(err)
	if code == "" || !strings.HasSuffix(code, "Exception") {
		return ""
	}
	return code
}

func appendBedrockFailureDiagnostic(output *types.AssistantMessage, err error, fallbackRequestID *string) {
	details := map[string]any{}
	if err != nil {
		var statusErr interface{ HTTPStatusCode() int }
		if errors.As(err, &statusErr) {
			details["status"] = statusErr.HTTPStatusCode()
		}
		if code := extractBedrockErrorCode(err); code != "" {
			details["errorCode"] = code
		}
	}
	requestID := fallbackRequestID
	if err != nil {
		var responseErr interface{ HTTPResponse() *smithyhttp.Response }
		if errors.As(err, &responseErr) && responseErr.HTTPResponse() != nil {
			if value := responseErr.HTTPResponse().Header.Get("x-amzn-requestid"); value != "" {
				requestID = normalizeBedrockDiagnosticValue(value)
			}
		}
	}
	if requestID != nil {
		details["requestId"] = *requestID
	}
	if len(details) == 0 {
		return
	}
	output.Diagnostics = append(output.Diagnostics, types.AssistantMessageDiagnostic{
		Type:      "bedrock_response_failure",
		Timestamp: nowMillis(),
		Details:   details,
	})
}

// bedrockInt32Value dereferences an SDK int32 pointer, treating nil as zero.
func bedrockInt32Value(value *int32) float64 {
	if value == nil {
		return 0
	}
	return float64(*value)
}

// bedrockInt32Index dereferences an SDK int32 pointer as an int index.
func bedrockInt32Index(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}
