// This file is a Go port of packages/ai/src/utils/assistant-message-frame.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The frame variant payloads are flattened into optional fields so one value
// type carries the whole protocol without losing ordering or presence.
package utils

import (
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
)

// AssistantMessageFrameType discriminates a compact assistant-message frame.
type AssistantMessageFrameType string

// Frame types.
const (
	FrameStart           AssistantMessageFrameType = "start"
	FrameTextStart       AssistantMessageFrameType = "text_start"
	FrameTextDelta       AssistantMessageFrameType = "text_delta"
	FrameTextEnd         AssistantMessageFrameType = "text_end"
	FrameThinkingStart   AssistantMessageFrameType = "thinking_start"
	FrameThinkingDelta   AssistantMessageFrameType = "thinking_delta"
	FrameThinkingEnd     AssistantMessageFrameType = "thinking_end"
	FrameToolCallStart   AssistantMessageFrameType = "toolcall_start"
	FrameToolCallCheckpt AssistantMessageFrameType = "toolcall_checkpoint"
	FrameToolCallDelta   AssistantMessageFrameType = "toolcall_delta"
	FrameToolCallEnd     AssistantMessageFrameType = "toolcall_end"
)

// AssistantMessageFrame is a compact, replayable assistant-message progress
// record. Terminal settlement is intentionally excluded and must be persisted
// separately.
type AssistantMessageFrame struct {
	Type AssistantMessageFrameType `json:"type"`
	// Partial is the message snapshot for the start frame.
	Partial *types.AssistantMessage `json:"partial,omitempty"`
	// ContentIndex indexes into the replayed message's content.
	ContentIndex *int `json:"contentIndex,omitempty"`
	// Content is the text of a text block (text_start) or the authoritative end
	// text (text_end).
	Content *string `json:"content,omitempty"`
	// TextSignature is carried by text_end.
	TextSignature *string `json:"textSignature,omitempty"`
	// Thinking is the initial thinking content for thinking_start.
	Thinking *types.ThinkingContent `json:"thinkingContent,omitempty"`
	// ThinkingSignature and Redacted are carried by thinking_end.
	ThinkingSignature *string `json:"thinkingSignature,omitempty"`
	Redacted          *bool   `json:"redacted,omitempty"`
	// ToolCall is the initial tool call for toolcall_start.
	ToolCall *types.ToolCall `json:"toolCall,omitempty"`
	// JSON is the checkpoint JSON for toolcall_checkpoint.
	JSON string `json:"json,omitempty"`
	// Delta is the incremental text for the delta frames.
	Delta *string `json:"delta,omitempty"`
	// ID, Name and Arguments are carried by toolcall_end.
	ID               *string         `json:"id,omitempty"`
	Name             *string         `json:"name,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature *string         `json:"thoughtSignature,omitempty"`
	Namespace        *string         `json:"namespace,omitempty"`
}

func cloneTextContent(content types.TextContent) types.TextContent {
	cloned := types.TextContent{Type: types.ContentTypeText, Text: content.Text}
	if content.TextSignature != nil {
		value := *content.TextSignature
		cloned.TextSignature = &value
	}
	return cloned
}

func cloneThinkingContent(content types.ThinkingContent) types.ThinkingContent {
	cloned := types.ThinkingContent{Type: types.ContentTypeThinking, Thinking: content.Thinking}
	if content.ThinkingSignature != nil {
		value := *content.ThinkingSignature
		cloned.ThinkingSignature = &value
	}
	if content.Redacted != nil {
		value := *content.Redacted
		cloned.Redacted = &value
	}
	return cloned
}

func cloneToolCall(call types.ToolCall) types.ToolCall {
	cloned := types.ToolCall{
		Type:      types.ContentTypeToolCall,
		Id:        call.Id,
		Name:      call.Name,
		Arguments: append(json.RawMessage(nil), call.Arguments...),
	}
	if call.ThoughtSignature != nil {
		value := *call.ThoughtSignature
		cloned.ThoughtSignature = &value
	}
	if call.Namespace != nil {
		value := *call.Namespace
		cloned.Namespace = &value
	}
	return cloned
}

func cloneStartMessage(message types.AssistantMessage) types.AssistantMessage {
	cloned := types.AssistantMessage{
		Role:       types.AssistantMessageRole,
		Content:    []types.ContentBlock{},
		Api:        message.Api,
		Provider:   message.Provider,
		Model:      message.Model,
		Usage:      message.Usage,
		StopReason: types.StopReasonPending,
		Timestamp:  message.Timestamp,
	}
	if message.ResponseModel != nil {
		value := *message.ResponseModel
		cloned.ResponseModel = &value
	}
	if message.ResponseId != nil {
		value := *message.ResponseId
		cloned.ResponseId = &value
	}
	if message.ProviderThinkingLevel != nil {
		value := *message.ProviderThinkingLevel
		cloned.ProviderThinkingLevel = &value
	}
	if message.Diagnostics != nil {
		cloned.Diagnostics = append([]types.AssistantMessageDiagnostic(nil), message.Diagnostics...)
	}
	return cloned
}

func assertContentIndex(contentIndex int) error {
	if contentIndex < 0 {
		return fmt.Errorf("Invalid assistant message frame contentIndex: %d", contentIndex)
	}
	return nil
}

// serializedArguments serializes tool-call arguments, erroring when they are not
// JSON-serializable.
func serializedArguments(arguments json.RawMessage) (string, error) {
	if len(arguments) == 0 {
		return "{}", nil
	}
	if !json.Valid(arguments) {
		return "", fmt.Errorf("Tool-call arguments are not JSON-serializable")
	}
	return string(arguments), nil
}

func emptyParsedToolArguments() string {
	serialized, _ := serializedArguments(nil)
	return serialized
}

// encoderBlockState is the per-block encoder state.
type encoderBlockState struct {
	kind string // "text", "thinking", "toolCall"

	coveredChars int
	deltaChars   int

	caughtUp          bool
	catchupJSON       string
	snapshotArguments string
}

// AssistantMessageFrameEncoder encodes one assistant stream. `partial` remains a
// shared live accumulator; the encoder uses per-block offsets to avoid replaying
// deltas already visible when an older queued event is consumed.
type AssistantMessageFrameEncoder struct {
	started  bool
	terminal bool
	blocks   map[int]*encoderBlockState
}

// NewAssistantMessageFrameEncoder creates a frame encoder.
func NewAssistantMessageFrameEncoder() *AssistantMessageFrameEncoder {
	return &AssistantMessageFrameEncoder{blocks: map[int]*encoderBlockState{}}
}

// Encode converts one assistant stream event into a compact frame, or nil when
// the event carries no frame (done/error, or an already-covered delta).
func (e *AssistantMessageFrameEncoder) Encode(event types.AssistantMessageEvent) (*AssistantMessageFrame, error) {
	if e.terminal {
		return nil, fmt.Errorf("Assistant message event %s follows a terminal event", event.Type)
	}

	switch event.Type {
	case types.AssistantEventStart:
		if e.started {
			return nil, fmt.Errorf("Assistant message stream contains more than one start event")
		}
		e.started = true
		if event.Partial == nil {
			return nil, fmt.Errorf("start event has no partial message")
		}
		cloned := cloneStartMessage(*event.Partial)
		return &AssistantMessageFrame{Type: FrameStart, Partial: &cloned}, nil
	case types.AssistantEventDone:
		if !e.started {
			return nil, fmt.Errorf("Assistant message done event appears before start")
		}
		e.terminal = true
		return nil, nil
	case types.AssistantEventError:
		e.terminal = true
		return nil, nil
	}

	if !e.started {
		return nil, fmt.Errorf("Assistant message %s event appears before start", event.Type)
	}

	switch event.Type {
	case types.AssistantEventTextStart:
		content, err := eventBlock(event)
		if err != nil {
			return nil, err
		}
		if content.Type != types.ContentTypeText || content.Text == nil {
			return nil, fmt.Errorf("text_start event points to %s block at index %d", content.Type, *event.ContentIndex)
		}
		e.startBlock(*event.ContentIndex, &encoderBlockState{kind: "text", coveredChars: len(utf16Units(content.Text.Text))})
		cloned := cloneTextContent(*content.Text)
		return &AssistantMessageFrame{Type: FrameTextStart, ContentIndex: event.ContentIndex, Content: &cloned.Text}, nil
	}
	return e.encodeTail(event)
}

// encodeTail handles the remaining frame events.
func (e *AssistantMessageFrameEncoder) encodeTail(event types.AssistantMessageEvent) (*AssistantMessageFrame, error) {
	switch event.Type {
	case types.AssistantEventTextDelta:
		return e.encodeTextDelta(*event.ContentIndex, deltaOf(event), "text")
	case types.AssistantEventTextEnd:
		content, err := eventBlock(event)
		if err != nil {
			return nil, err
		}
		if content.Type != types.ContentTypeText || content.Text == nil {
			return nil, fmt.Errorf("text_end event points to %s block at index %d", content.Type, *event.ContentIndex)
		}
		if err := e.endBlock(*event.ContentIndex, "text"); err != nil {
			return nil, err
		}
		frame := &AssistantMessageFrame{Type: FrameTextEnd, ContentIndex: event.ContentIndex, Content: event.ContentBlock}
		if content.Text.TextSignature != nil {
			value := *content.Text.TextSignature
			frame.TextSignature = &value
		}
		return frame, nil
	case types.AssistantEventThinkingStart:
		content, err := eventBlock(event)
		if err != nil {
			return nil, err
		}
		if content.Type != types.ContentTypeThinking || content.Thinking == nil {
			return nil, fmt.Errorf("thinking_start event points to %s block at index %d", content.Type, *event.ContentIndex)
		}
		e.startBlock(*event.ContentIndex, &encoderBlockState{kind: "thinking", coveredChars: len(utf16Units(content.Thinking.Thinking))})
		cloned := cloneThinkingContent(*content.Thinking)
		return &AssistantMessageFrame{Type: FrameThinkingStart, ContentIndex: event.ContentIndex, Thinking: &cloned}, nil
	case types.AssistantEventThinkingDelta:
		return e.encodeTextDelta(*event.ContentIndex, deltaOf(event), "thinking")
	case types.AssistantEventThinkingEnd:
		content, err := eventBlock(event)
		if err != nil {
			return nil, err
		}
		if content.Type != types.ContentTypeThinking || content.Thinking == nil {
			return nil, fmt.Errorf("thinking_end event points to %s block at index %d", content.Type, *event.ContentIndex)
		}
		if err := e.endBlock(*event.ContentIndex, "thinking"); err != nil {
			return nil, err
		}
		frame := &AssistantMessageFrame{Type: FrameThinkingEnd, ContentIndex: event.ContentIndex, Content: event.ContentBlock}
		if content.Thinking.ThinkingSignature != nil {
			value := *content.Thinking.ThinkingSignature
			frame.ThinkingSignature = &value
		}
		if content.Thinking.Redacted != nil {
			value := *content.Thinking.Redacted
			frame.Redacted = &value
		}
		return frame, nil
	case types.AssistantEventToolCallStart:
		content, err := eventBlock(event)
		if err != nil {
			return nil, err
		}
		if content.Type != types.ContentTypeToolCall || content.ToolCall == nil {
			return nil, fmt.Errorf("toolcall_start event points to %s block at index %d", content.Type, *event.ContentIndex)
		}
		snapshotArguments, err := serializedArguments(content.ToolCall.Arguments)
		if err != nil {
			return nil, err
		}
		caughtUp := snapshotArguments == emptyParsedToolArguments()
		state := &encoderBlockState{kind: "toolCall", caughtUp: caughtUp}
		if !caughtUp {
			state.snapshotArguments = snapshotArguments
		}
		e.startBlock(*event.ContentIndex, state)
		cloned := cloneToolCall(*content.ToolCall)
		return &AssistantMessageFrame{Type: FrameToolCallStart, ContentIndex: event.ContentIndex, ToolCall: &cloned}, nil
	case types.AssistantEventToolCallDelta:
		return e.encodeToolCallDelta(*event.ContentIndex, deltaOf(event))
	case types.AssistantEventToolCallEnd:
		content, err := eventBlock(event)
		if err != nil {
			return nil, err
		}
		if content.Type != types.ContentTypeToolCall {
			return nil, fmt.Errorf("toolcall_end event points to %s block at index %d", content.Type, *event.ContentIndex)
		}
		if event.ToolCall == nil || event.ToolCall.Type != types.ContentTypeToolCall {
			return nil, fmt.Errorf("toolcall_end event has invalid tool call at index %d", *event.ContentIndex)
		}
		if err := e.endBlock(*event.ContentIndex, "toolCall"); err != nil {
			return nil, err
		}
		frame := &AssistantMessageFrame{
			Type:         FrameToolCallEnd,
			ContentIndex: event.ContentIndex,
			ID:           &event.ToolCall.Id,
			Name:         &event.ToolCall.Name,
			Arguments:    append(json.RawMessage(nil), event.ToolCall.Arguments...),
		}
		if event.ToolCall.ThoughtSignature != nil {
			value := *event.ToolCall.ThoughtSignature
			frame.ThoughtSignature = &value
		}
		if event.ToolCall.Namespace != nil {
			value := *event.ToolCall.Namespace
			frame.Namespace = &value
		}
		return frame, nil
	default:
		return nil, fmt.Errorf("unexpected assistant message event %s", event.Type)
	}
}

func deltaOf(event types.AssistantMessageEvent) string {
	if event.Delta != nil {
		return *event.Delta
	}
	return ""
}

func eventBlock(event types.AssistantMessageEvent) (types.ContentBlock, error) {
	if event.ContentIndex == nil {
		return types.ContentBlock{}, fmt.Errorf("%s event has no contentIndex", event.Type)
	}
	if err := assertContentIndex(*event.ContentIndex); err != nil {
		return types.ContentBlock{}, err
	}
	if event.Partial == nil || *event.ContentIndex >= len(event.Partial.Content) {
		return types.ContentBlock{}, fmt.Errorf("%s event has no content block at index %d", event.Type, *event.ContentIndex)
	}
	return event.Partial.Content[*event.ContentIndex], nil
}

func (e *AssistantMessageFrameEncoder) startBlock(contentIndex int, state *encoderBlockState) error {
	if err := assertContentIndex(contentIndex); err != nil {
		return err
	}
	if _, exists := e.blocks[contentIndex]; exists {
		return fmt.Errorf("Assistant message block %d starts more than once", contentIndex)
	}
	e.blocks[contentIndex] = state
	return nil
}

func (e *AssistantMessageFrameEncoder) block(contentIndex int, kind string) (*encoderBlockState, error) {
	if err := assertContentIndex(contentIndex); err != nil {
		return nil, err
	}
	state, ok := e.blocks[contentIndex]
	if !ok {
		return nil, fmt.Errorf("Assistant message %s block %d has not started", kind, contentIndex)
	}
	if state.kind != kind {
		return nil, fmt.Errorf("Assistant message block %d is %s, not %s", contentIndex, state.kind, kind)
	}
	return state, nil
}

func (e *AssistantMessageFrameEncoder) endBlock(contentIndex int, kind string) error {
	if _, err := e.block(contentIndex, kind); err != nil {
		return err
	}
	delete(e.blocks, contentIndex)
	return nil
}

func (e *AssistantMessageFrameEncoder) encodeTextDelta(contentIndex int, delta, kind string) (*AssistantMessageFrame, error) {
	state, err := e.block(contentIndex, kind)
	if err != nil {
		return nil, err
	}
	deltaStart := state.deltaChars
	state.deltaChars += len(utf16Units(delta))
	covered := state.coveredChars - deltaStart
	if covered < 0 {
		covered = 0
	}
	if covered >= len(utf16Units(delta)) {
		return nil, nil
	}
	uncovered := delta
	if covered != 0 {
		units := utf16Units(delta)
		uncovered = string(unitsToUTF8(units[covered:]))
	}
	frameType := FrameTextDelta
	if kind == "thinking" {
		frameType = FrameThinkingDelta
	}
	index := contentIndex
	return &AssistantMessageFrame{Type: frameType, ContentIndex: &index, Delta: &uncovered}, nil
}

func (e *AssistantMessageFrameEncoder) encodeToolCallDelta(contentIndex int, delta string) (*AssistantMessageFrame, error) {
	state, err := e.block(contentIndex, "toolCall")
	if err != nil {
		return nil, err
	}
	if state.caughtUp {
		if delta == "" {
			return nil, nil
		}
		index := contentIndex
		return &AssistantMessageFrame{Type: FrameToolCallDelta, ContentIndex: &index, Delta: &delta}, nil
	}
	state.catchupJSON += delta
	argumentsValue := ParseStreamingJSON(state.catchupJSON)
	serialized, err := serializedArguments(marshalRaw(argumentsValue))
	if err != nil {
		return nil, err
	}
	if serialized != state.snapshotArguments {
		// Legacy grammar calls include the initial input in toolcall_start, but
		// their JSON delta stream still begins at an empty input. Its parsed
		// arguments can therefore extend rather than exactly reproduce the
		// start snapshot.
		snapshotValue := ParseStreamingJSON(state.snapshotArguments)
		if !isJSONPrefix(snapshotValue, argumentsValue) {
			return nil, nil
		}
	}
	state.caughtUp = true
	state.snapshotArguments = ""
	jsonText := state.catchupJSON
	state.catchupJSON = ""
	if jsonText == "" {
		return nil, nil
	}
	index := contentIndex
	return &AssistantMessageFrame{Type: FrameToolCallCheckpt, ContentIndex: &index, JSON: jsonText}, nil
}

// marshalRaw serializes a JSON value back into a RawMessage for comparison.
func marshalRaw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

// isJSONPrefix reports whether snapshot is a structural prefix of current.
func isJSONPrefix(snapshot, current any) bool {
	switch snap := snapshot.(type) {
	case string:
		cur, ok := current.(string)
		return ok && len(cur) >= len(snap) && cur[:len(snap)] == snap
	case []any:
		cur, ok := current.([]any)
		if !ok || len(snap) > len(cur) {
			return false
		}
		for i := range snap {
			if !isJSONPrefix(snap[i], cur[i]) {
				return false
			}
		}
		return true
	case nil:
		return current == nil
	case map[string]any:
		cur, ok := current.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range snap {
			curValue, ok := cur[key]
			if !ok || !isJSONPrefix(value, curValue) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprint(snapshot) == fmt.Sprint(current)
	}
}

// reducerBlockState is the per-block reducer state.
type reducerBlockState struct {
	kind  string // "text", "thinking", "toolCall"
	ended bool
	json  string
}

// ReduceAssistantMessageFrames replays compact frames without mutating them. It
// returns nil when the iterable contains no start frame.
func ReduceAssistantMessageFrames(frames []AssistantMessageFrame) (*types.AssistantMessage, error) {
	var message *types.AssistantMessage
	frameBeforeStart := ""
	states := map[int]*reducerBlockState{}

	for _, frame := range frames {
		if frame.Type == FrameStart {
			if message != nil {
				return nil, fmt.Errorf("Assistant message frame sequence contains more than one start frame")
			}
			if frameBeforeStart != "" {
				return nil, fmt.Errorf("%s frame appears before the start frame", frameBeforeStart)
			}
			if frame.Partial == nil {
				return nil, fmt.Errorf("start frame has no partial message")
			}
			cloned := cloneStartMessage(*frame.Partial)
			message = &cloned
			continue
		}
		if message == nil {
			if frameBeforeStart == "" {
				frameBeforeStart = string(frame.Type)
			}
			continue
		}

		if err := reduceFrame(message, states, frame); err != nil {
			return nil, err
		}
	}

	if message == nil {
		return nil, nil
	}
	for contentIndex, state := range states {
		if state.kind != "toolCall" || state.ended || state.json == "" {
			continue
		}
		if contentIndex >= len(message.Content) {
			return nil, fmt.Errorf("Unreachable tool-call frame state")
		}
		block := &message.Content[contentIndex]
		if block.Type != types.ContentTypeToolCall || block.ToolCall == nil {
			return nil, fmt.Errorf("Unreachable tool-call frame state")
		}
		block.ToolCall.Arguments = marshalRaw(ParseStreamingJSON(state.json))
	}
	return message, nil
}

func reduceFrame(message *types.AssistantMessage, states map[int]*reducerBlockState, frame AssistantMessageFrame) error {
	index := 0
	if frame.ContentIndex != nil {
		index = *frame.ContentIndex
	}
	switch frame.Type {
	case FrameTextStart:
		if frame.Content == nil {
			return fmt.Errorf("text_start frame has no content")
		}
		return appendBlock(message, states, index, types.TextBlock(*frame.Content), &reducerBlockState{kind: "text"})
	case FrameTextDelta:
		block, state, err := activeBlock(message, states, index, "text", frame.Type)
		if err != nil {
			return err
		}
		_ = state
		if block.Text == nil {
			return fmt.Errorf("Unreachable text frame state")
		}
		block.Text.Text += valueOr(frame.Delta, "")
		return nil
	case FrameTextEnd:
		block, state, err := activeBlock(message, states, index, "text", frame.Type)
		if err != nil {
			return err
		}
		if block.Text == nil {
			return fmt.Errorf("Unreachable text frame state")
		}
		block.Text.Text = valueOr(frame.Content, "")
		block.Text.TextSignature = nil
		if frame.TextSignature != nil {
			value := *frame.TextSignature
			block.Text.TextSignature = &value
		}
		state.ended = true
		return nil
	case FrameThinkingStart:
		if frame.Thinking == nil {
			return fmt.Errorf("thinking_start frame has no content")
		}
		return appendBlock(message, states, index, types.ThinkingBlock(frame.Thinking.Thinking), &reducerBlockState{kind: "thinking"})
	case FrameThinkingDelta:
		block, _, err := activeBlock(message, states, index, "thinking", frame.Type)
		if err != nil {
			return err
		}
		if block.Thinking == nil {
			return fmt.Errorf("Unreachable thinking frame state")
		}
		block.Thinking.Thinking += valueOr(frame.Delta, "")
		return nil
	case FrameThinkingEnd:
		block, state, err := activeBlock(message, states, index, "thinking", frame.Type)
		if err != nil {
			return err
		}
		if block.Thinking == nil {
			return fmt.Errorf("Unreachable thinking frame state")
		}
		block.Thinking.Thinking = valueOr(frame.Content, "")
		block.Thinking.ThinkingSignature = nil
		block.Thinking.Redacted = nil
		if frame.ThinkingSignature != nil {
			value := *frame.ThinkingSignature
			block.Thinking.ThinkingSignature = &value
		}
		if frame.Redacted != nil {
			value := *frame.Redacted
			block.Thinking.Redacted = &value
		}
		state.ended = true
		return nil
	case FrameToolCallStart:
		if frame.ToolCall == nil {
			return fmt.Errorf("toolcall_start frame has no tool call")
		}
		call := cloneToolCall(*frame.ToolCall)
		return appendBlock(message, states, index, types.ToolCallBlock(call), &reducerBlockState{kind: "toolCall"})
	case FrameToolCallCheckpt:
		block, state, err := activeBlock(message, states, index, "toolCall", frame.Type)
		if err != nil {
			return err
		}
		if block.ToolCall == nil {
			return fmt.Errorf("Unreachable tool-call checkpoint state")
		}
		state.json = frame.JSON
		block.ToolCall.Arguments = marshalRaw(ParseStreamingJSON(frame.JSON))
		return nil
	case FrameToolCallDelta:
		_, state, err := activeBlock(message, states, index, "toolCall", frame.Type)
		if err != nil {
			return err
		}
		state.json += valueOr(frame.Delta, "")
		return nil
	case FrameToolCallEnd:
		block, state, err := activeBlock(message, states, index, "toolCall", frame.Type)
		if err != nil {
			return err
		}
		if block.ToolCall == nil {
			return fmt.Errorf("Unreachable tool-call frame state")
		}
		block.ToolCall.Id = valueOr(frame.ID, "")
		block.ToolCall.Name = valueOr(frame.Name, "")
		block.ToolCall.Arguments = append(json.RawMessage(nil), frame.Arguments...)
		block.ToolCall.ThoughtSignature = nil
		block.ToolCall.Namespace = nil
		if frame.ThoughtSignature != nil {
			value := *frame.ThoughtSignature
			block.ToolCall.ThoughtSignature = &value
		}
		if frame.Namespace != nil {
			value := *frame.Namespace
			block.ToolCall.Namespace = &value
		}
		state.ended = true
		return nil
	default:
		return fmt.Errorf("unexpected assistant message frame %s", frame.Type)
	}
}

func appendBlock(message *types.AssistantMessage, states map[int]*reducerBlockState, contentIndex int, block types.ContentBlock, state *reducerBlockState) error {
	if err := assertContentIndex(contentIndex); err != nil {
		return err
	}
	if contentIndex != len(message.Content) {
		reason := "would leave a gap"
		if contentIndex < len(message.Content) {
			reason = "already exists"
		}
		return fmt.Errorf("Cannot start assistant message block at index %d: %s", contentIndex, reason)
	}
	message.Content = append(message.Content, cloneBlock(block))
	states[contentIndex] = state
	return nil
}

func activeBlock(message *types.AssistantMessage, states map[int]*reducerBlockState, contentIndex int, expectedKind string, frameType AssistantMessageFrameType) (*types.ContentBlock, *reducerBlockState, error) {
	if err := assertContentIndex(contentIndex); err != nil {
		return nil, nil, err
	}
	state, ok := states[contentIndex]
	if !ok || contentIndex >= len(message.Content) {
		return nil, nil, fmt.Errorf("%s frame has no started block at index %d", frameType, contentIndex)
	}
	block := &message.Content[contentIndex]
	if state.kind != expectedKind || string(block.Type) != expectedKind {
		return nil, nil, fmt.Errorf("%s frame expected %s block at index %d, found %s", frameType, expectedKind, contentIndex, block.Type)
	}
	if state.ended {
		return nil, nil, fmt.Errorf("%s frame follows the end of block at index %d", frameType, contentIndex)
	}
	return block, state, nil
}

func cloneBlock(block types.ContentBlock) types.ContentBlock {
	switch block.Type {
	case types.ContentTypeText:
		if block.Text != nil {
			cloned := cloneTextContent(*block.Text)
			return types.ContentBlock{Type: types.ContentTypeText, Text: &cloned}
		}
	case types.ContentTypeThinking:
		if block.Thinking != nil {
			cloned := cloneThinkingContent(*block.Thinking)
			return types.ContentBlock{Type: types.ContentTypeThinking, Thinking: &cloned}
		}
	case types.ContentTypeToolCall:
		if block.ToolCall != nil {
			cloned := cloneToolCall(*block.ToolCall)
			return types.ContentBlock{Type: types.ContentTypeToolCall, ToolCall: &cloned}
		}
	}
	return block
}

func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
