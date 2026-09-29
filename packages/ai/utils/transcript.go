// This file is a Go port of packages/ai/src/utils/transcript.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"encoding/json"
	"reflect"

	"github.com/minifish-org/pith/packages/ai/types"
)

// TranscriptContext is re-exported from types for callers that import only the
// utils package.
type TranscriptContext = types.TranscriptContext

// CreateInitialSystemMessage builds the leading system message for a prompt and
// tool set. It returns nil when both are empty, so an empty transcript stays
// empty.
func CreateInitialSystemMessage(systemPrompt *string, tools []types.Tool) *types.SystemMessage {
	hasSystemPrompt := systemPrompt != nil && len(*systemPrompt) > 0
	hasTools := len(tools) > 0
	if !hasSystemPrompt && !hasTools {
		return nil
	}
	message := types.SystemMessage{Role: types.SystemMessageRole, Timestamp: 0}
	if systemPrompt != nil {
		message.Content = types.SystemContentText(*systemPrompt)
	} else {
		message.Content = types.SystemContentText("")
	}
	if hasTools {
		message.ToolsAdded = append([]types.Tool(nil), tools...)
	}
	return &message
}

// NormalizeContext folds Context.SystemPrompt and Context.Tools into a leading
// system message. This is the only entry point that produces a TranscriptContext.
func NormalizeContext(context types.Context) *types.TranscriptContext {
	initial := CreateInitialSystemMessage(context.SystemPrompt, context.Tools)
	messages := make([]types.Message, 0, len(context.Messages)+1)
	if initial != nil {
		messages = append(messages, types.NewSystemMessageVariant(*initial))
	}
	messages = append(messages, context.Messages...)
	return types.NewTranscriptContext(messages)
}

// TranscriptMessages is any message list. The replay helpers only read entries
// whose role is "system", so agent transcripts that carry custom message roles
// can be passed without filtering.
type TranscriptMessages = []types.Message

func isSystemMessage(message types.Message) bool { return message.Role == types.SystemMessageRole }

// GetInitialSystemMessage returns the leading system message, if the transcript
// starts with one.
func GetInitialSystemMessage(messages TranscriptMessages) *types.SystemMessage {
	if len(messages) == 0 {
		return nil
	}
	first := messages[0]
	if first.Role != types.SystemMessageRole {
		return nil
	}
	return first.System
}

// WithoutInitialSystemMessage drops the leading system message for APIs that
// carry the prompt outside the message list.
func WithoutInitialSystemMessage(messages []types.Message) []types.Message {
	if GetInitialSystemMessage(messages) != nil {
		return messages[1:]
	}
	return messages
}

// GetCurrentTools resolves the tools available after applying every transcript
// delta in order.
func GetCurrentTools(messages TranscriptMessages) []types.Tool {
	order := []string{}
	tools := map[string]types.Tool{}
	for _, message := range messages {
		if !isSystemMessage(message) || message.System == nil {
			continue
		}
		for _, tool := range message.System.ToolsRemoved {
			if _, ok := tools[tool.Name]; ok {
				delete(tools, tool.Name)
				order = removeName(order, tool.Name)
			}
		}
		for _, tool := range message.System.ToolsAdded {
			if _, ok := tools[tool.Name]; !ok {
				order = append(order, tool.Name)
			}
			tools[tool.Name] = tool
		}
	}
	out := make([]types.Tool, 0, len(order))
	for _, name := range order {
		out = append(out, tools[name])
	}
	return out
}

func removeName(names []string, target string) []string {
	out := names[:0]
	for _, name := range names {
		if name != target {
			out = append(out, name)
		}
	}
	return out
}

// GetCurrentSystemMessage replays every system message into one leading system
// message holding the current prompt and tools.
func GetCurrentSystemMessage(messages TranscriptMessages) *types.SystemMessage {
	content := []string{}
	sections := map[string]*string{}
	sectionOrder := []string{}
	hasTimestamp := false
	timestamp := 0.0
	for _, message := range messages {
		if !isSystemMessage(message) || message.System == nil {
			continue
		}
		system := message.System
		if !hasTimestamp {
			timestamp = system.Timestamp
			hasTimestamp = true
		}
		if text := ContentText(system.Content); len(text) > 0 {
			content = append(content, text)
		}
		for _, name := range sortedSectionNames(system.Sections) {
			value := system.Sections[name]
			if value == nil {
				delete(sections, name)
				sectionOrder = removeName(sectionOrder, name)
			} else {
				if _, ok := sections[name]; !ok {
					sectionOrder = append(sectionOrder, name)
				}
				sections[name] = value
			}
		}
	}
	tools := GetCurrentTools(messages)
	if !hasTimestamp && len(tools) == 0 {
		return nil
	}
	message := types.SystemMessage{Role: types.SystemMessageRole, Timestamp: timestamp}
	message.Content = types.SystemContentText(joinStrings(content))
	if len(sectionOrder) > 0 {
		message.Sections = types.SystemSections{}
		for _, name := range sectionOrder {
			message.Sections[name] = sections[name]
		}
	}
	if len(tools) > 0 {
		message.ToolsAdded = tools
	}
	return &message
}

func joinStrings(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += "\n\n"
		}
		out += part
	}
	return out
}

// GetCurrentSystemPrompt renders the current system prompt text after replaying
// every system message.
func GetCurrentSystemPrompt(messages TranscriptMessages) string {
	message := GetCurrentSystemMessage(messages)
	if message == nil {
		return ""
	}
	return GetSystemMessageText(*message)
}

// CollapseSystemMessages rebuilds the transcript for APIs without
// mid-conversation system messages: the replayed system message leads, and every
// later system message is dropped.
func CollapseSystemMessages(context types.TranscriptContext) types.TranscriptContext {
	head := GetCurrentSystemMessage(context.Messages)
	messages := make([]types.Message, 0, len(context.Messages)+1)
	if head != nil {
		messages = append(messages, types.NewSystemMessageVariant(*head))
	}
	for _, message := range context.Messages {
		if message.Role == types.SystemMessageRole {
			continue
		}
		messages = append(messages, message)
	}
	return *types.NewTranscriptContext(messages)
}

// ResolveTranscript keeps later system messages in place when the model accepts
// them; otherwise it collapses them.
func ResolveTranscript(context types.TranscriptContext, supportsMidConvoSystemMessages *bool) types.TranscriptContext {
	if supportsMidConvoSystemMessages != nil && *supportsMidConvoSystemMessages {
		return context
	}
	return CollapseSystemMessages(context)
}

// ToToolDeclaration strips executable and display-only fields from a tool before
// transcript comparison or persistence.
func ToToolDeclaration(tool types.Tool) types.Tool {
	declaration := types.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		Input:       cloneToolInput(tool.Input),
	}
	if tool.ConstrainedSampling != nil {
		config := *tool.ConstrainedSampling
		declaration.ConstrainedSampling = &config
	}
	declaration.ConstrainedDisabled = tool.ConstrainedDisabled
	return declaration
}

func cloneToolInput(input types.ToolInput) types.ToolInput {
	cloned := types.ToolInput{Type: input.Type}
	if len(input.Schema) > 0 {
		cloned.Schema = append(json.RawMessage(nil), input.Schema...)
	}
	if input.Grammar != nil {
		value := *input.Grammar
		cloned.Grammar = &value
	}
	return cloned
}

// DeclarationsEqual reports whether two tools declare the same interface to the
// model. Both sides go through ToToolDeclaration first, and the serialized
// declarations are compared.
func DeclarationsEqual(left, right types.Tool) bool {
	leftJSON, leftErr := json.Marshal(ToToolDeclaration(left))
	rightJSON, rightErr := json.Marshal(ToToolDeclaration(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

// ToolStateChanges is the delta between two complete tool states.
type ToolStateChanges struct {
	ToolsAdded   []types.Tool          `json:"toolsAdded"`
	ToolsRemoved []types.ToolReference `json:"toolsRemoved"`
}

// GetToolStateChanges compares two complete tool states. A changed definition is
// a removal followed by an addition.
func GetToolStateChanges(previous, current []types.Tool) ToolStateChanges {
	previousTools := map[string]types.Tool{}
	for _, tool := range previous {
		previousTools[tool.Name] = tool
	}
	currentTools := map[string]types.Tool{}
	for _, tool := range current {
		currentTools[tool.Name] = tool
	}

	added := []types.Tool{}
	for _, tool := range current {
		previousTool, ok := previousTools[tool.Name]
		if !ok || !DeclarationsEqual(previousTool, tool) {
			added = append(added, ToToolDeclaration(tool))
		}
	}
	removed := []types.ToolReference{}
	for _, tool := range previous {
		currentTool, ok := currentTools[tool.Name]
		if !ok || !DeclarationsEqual(tool, currentTool) {
			removed = append(removed, types.ToolReference{Name: tool.Name})
		}
	}
	return ToolStateChanges{ToolsAdded: added, ToolsRemoved: removed}
}

// GetDeclaredTools returns every definition referenced by transcript tool state,
// in first-declaration order.
func GetDeclaredTools(messages TranscriptMessages) []types.Tool {
	order := []string{}
	definitions := map[string]types.Tool{}
	for _, message := range messages {
		if !isSystemMessage(message) || message.System == nil {
			continue
		}
		for _, tool := range message.System.ToolsAdded {
			if _, ok := definitions[tool.Name]; !ok {
				order = append(order, tool.Name)
			}
			definitions[tool.Name] = tool
		}
	}
	out := make([]types.Tool, 0, len(order))
	for _, name := range order {
		out = append(out, definitions[name])
	}
	return out
}

// HasToolRedefinitions reports whether a tool name was declared twice with
// different definitions.
func HasToolRedefinitions(messages TranscriptMessages) bool {
	declared := map[string]types.Tool{}
	for _, message := range messages {
		if !isSystemMessage(message) || message.System == nil {
			continue
		}
		for _, tool := range message.System.ToolsAdded {
			previous, ok := declared[tool.Name]
			if ok && !DeclarationsEqual(previous, tool) {
				return true
			}
			declared[tool.Name] = tool
		}
	}
	return false
}

// HasNonAdditiveToolChanges reports whether tool history contains a removal or
// same-name redeclaration that an addition-only transport cannot replay.
func HasNonAdditiveToolChanges(messages TranscriptMessages) bool {
	declared := map[string]bool{}
	for _, message := range messages {
		if !isSystemMessage(message) || message.System == nil {
			continue
		}
		if len(message.System.ToolsRemoved) > 0 {
			return true
		}
		for _, tool := range message.System.ToolsAdded {
			if declared[tool.Name] {
				return true
			}
			declared[tool.Name] = true
		}
	}
	return false
}

// TranscriptTools splits tool declarations between the top-level request field
// and in-place additions.
type TranscriptTools struct {
	// RequestTools are tools sent in the top-level request field.
	RequestTools []types.Tool
	// AnchorsAdditions reports whether later system messages carry their own
	// ToolsAdded as in-place additions. When false, RequestTools already holds
	// the complete current tool set.
	AnchorsAdditions bool
}

// ResolveTranscriptTools splits tool declarations between the top-level request
// field and in-place additions. Transports that can anchor additions at a system
// message keep the initial tools at the top and load later ones where they
// appear; that only works when no tool was removed or redeclared.
func ResolveTranscriptTools(messages TranscriptMessages, supportsToolAdditions bool) TranscriptTools {
	anchorsAdditions := supportsToolAdditions && !HasNonAdditiveToolChanges(messages)
	if anchorsAdditions {
		initial := GetInitialSystemMessage(messages)
		if initial != nil {
			return TranscriptTools{RequestTools: initial.ToolsAdded, AnchorsAdditions: true}
		}
		return TranscriptTools{RequestTools: []types.Tool{}, AnchorsAdditions: true}
	}
	return TranscriptTools{RequestTools: GetCurrentTools(messages), AnchorsAdditions: false}
}

// DeclarationsDeepEqual is a structural comparison used by the self-tests when
// the serialized form is not the property under test.
func DeclarationsDeepEqual(left, right types.Tool) bool {
	return reflect.DeepEqual(ToToolDeclaration(left), ToToolDeclaration(right))
}
